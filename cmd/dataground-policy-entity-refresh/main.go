package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

const maximumEntitySnapshotBytes = 1 << 20

type refreshRequest struct {
	operation         string
	isolationDomainID string
	serviceID         string
	revisionID        string
	generation        int64
	entityFile        string
	installedDigest   []byte
	actorID           string
	reason            string
	correlationID     string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround invocation authorization entity refresh failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("entity refresh context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATAGROUND_DATABASE_URL is required")
	}
	database, err := persistence.OpenSQL(operationCtx, databaseURL)
	if err != nil {
		return err
	}
	if err := persistence.RequireCurrentSchema(operationCtx, database); err != nil {
		database.Close()
		return err
	}
	if err := database.Close(); err != nil {
		return err
	}
	pool, err := persistence.OpenPool(operationCtx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	reasonDigest := sha256.Sum256([]byte(request.reason))
	if request.operation == "publish" {
		entities, err := readEntitySnapshot(request.entityFile)
		if err != nil {
			return err
		}
		entityDigest := sha256.Sum256(entities)
		return repository.PublishInvocationAuthorizationEntityGeneration(
			operationCtx,
			persistence.InvocationAuthorizationEntityGeneration{
				Contract:          persistence.InvocationAuthorizationEntityGenerationContract,
				IsolationDomainID: request.isolationDomainID,
				ServiceID:         request.serviceID, RevisionID: request.revisionID,
				Generation: request.generation, EntityDigest: entityDigest[:],
				Entities: entities, PublishedBy: request.actorID,
				CorrelationID: request.correlationID, ReasonDigest: reasonDigest[:],
			},
		)
	}
	return repository.ActivateInvocationAuthorizationEntityGeneration(
		operationCtx,
		persistence.InvocationAuthorizationEntityActivation{
			Contract:          persistence.InvocationAuthorizationEntityActivationContract,
			IsolationDomainID: request.isolationDomainID,
			ServiceID:         request.serviceID, RevisionID: request.revisionID,
			Generation:            request.generation,
			InstalledPolicyDigest: request.installedDigest,
			ActivatedBy:           request.actorID, CorrelationID: request.correlationID,
			ReasonDigest: reasonDigest[:],
		},
	)
}

func parseArguments(arguments []string) (refreshRequest, error) {
	var request refreshRequest
	var generation uint64
	var installedDigest string
	flags := flag.NewFlagSet("dataground-policy-entity-refresh", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&request.operation, "operation", "", "publish or activate")
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain")
	flags.StringVar(&request.serviceID, "service", "", "exact agent service")
	flags.StringVar(&request.revisionID, "revision", "", "exact service revision")
	flags.Uint64Var(&generation, "generation", 0, "next sequential entity generation")
	flags.StringVar(&request.entityFile, "entity-file", "", "owner-only canonical Cedar entity snapshot")
	flags.StringVar(&installedDigest, "policy-digest", "", "installed policy sha256 digest")
	flags.StringVar(&request.actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&request.reason, "reason", "", "operator-visible refresh reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable operation correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return refreshRequest{}, err
	}
	if flags.NArg() != 0 ||
		(request.operation != "publish" && request.operation != "activate") ||
		request.isolationDomainID == "" || request.serviceID == "" || request.revisionID == "" ||
		generation == 0 || generation > math.MaxInt64 || request.actorID == "" ||
		request.reason == "" || request.correlationID == "" {
		return refreshRequest{}, errors.New("entity refresh flags are invalid or incomplete")
	}
	request.generation = int64(generation)
	if request.operation == "publish" {
		if request.entityFile == "" || installedDigest != "" {
			return refreshRequest{}, errors.New("publication requires only an entity file")
		}
		return request, nil
	}
	if request.entityFile != "" || installedDigest == "" {
		return refreshRequest{}, errors.New("activation requires only an installed policy digest")
	}
	decoded, err := parseDigest(installedDigest)
	if err != nil {
		return refreshRequest{}, err
	}
	request.installedDigest = decoded
	return request, nil
}

func parseDigest(value string) ([]byte, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || value != strings.ToLower(value) {
		return nil, errors.New("policy digest must be lowercase sha256:<hex>")
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("policy digest must be lowercase sha256:<hex>")
	}
	return decoded, nil
}

func readEntitySnapshot(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("entity snapshot is unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, errors.New("entity snapshot is unavailable")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(pathInfo, info) ||
		!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		info.Size() < 1 || info.Size() > maximumEntitySnapshotBytes {
		return nil, errors.New("entity snapshot must be a non-empty owner-only regular file no larger than 1 MiB")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumEntitySnapshotBytes+1))
	if err != nil || len(content) == 0 || len(content) > maximumEntitySnapshotBytes {
		return nil, errors.New("entity snapshot is unavailable")
	}
	return content, nil
}
