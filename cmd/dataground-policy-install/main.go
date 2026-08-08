package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

const (
	maximumPolicyFileBytes = 1 << 20
	maximumEntityFileBytes = 1 << 20
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	flags := flag.NewFlagSet("dataground-policy-install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var domainID, serviceID, revisionID, policySetID, policyFile, entityFile, actorID, reason, correlationID string
	flags.StringVar(&domainID, "isolation-domain", "", "isolation domain identifier")
	flags.StringVar(&serviceID, "service", "", "agent service identifier")
	flags.StringVar(&revisionID, "revision", "", "service revision identifier")
	flags.StringVar(&policySetID, "policy-set", "", "portable policy-set identifier")
	flags.StringVar(&policyFile, "policy-file", "", "Cedar policy file")
	flags.StringVar(&entityFile, "entity-file", "", "Cedar entity snapshot file")
	flags.StringVar(&actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&reason, "reason", "", "operator-visible installation reason")
	flags.StringVar(&correlationID, "correlation-id", "", "stable installation correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 ||
		domainID == "" ||
		serviceID == "" ||
		revisionID == "" ||
		policySetID == "" ||
		policyFile == "" ||
		entityFile == "" ||
		actorID == "" ||
		reason == "" ||
		correlationID == "" {
		return errors.New("all policy installation flags are required")
	}
	policyBytes, err := readPolicyFile(policyFile)
	if err != nil {
		return err
	}
	entityBytes, err := readEntityFile(entityFile)
	if err != nil {
		return err
	}
	policy, err := reconcile.NewInvocationAuthorizationPolicyWithEntities(
		reconcile.InvocationAuthorizationPolicyScope{
			IsolationDomainID: domainID,
			ServiceID:         serviceID,
			RevisionID:        revisionID,
		},
		policySetID,
		reconcile.CanonicalInvocationCedarEntitySchema(),
		policyBytes,
		entityBytes,
	)
	if err != nil {
		return err
	}
	if _, err := reconcile.NewStaticCedarInvocationAuthorizer(
		[]reconcile.InvocationAuthorizationPolicy{policy},
	); err != nil {
		return err
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATAGROUND_DATABASE_URL is required")
	}
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
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
	reasonDigest := sha256.Sum256([]byte(reason))
	return persistence.NewRepository(pool).InstallInvocationAuthorizationPolicy(
		operationCtx,
		persistence.InvocationAuthorizationPolicyRecord{
			Contract:                  policy.Contract,
			IsolationDomainID:         policy.IsolationDomainID,
			ServiceID:                 policy.ServiceID,
			RevisionID:                policy.RevisionID,
			PolicySetID:               policy.PolicySetID,
			PolicyDigest:              append([]byte(nil), policy.Digest[:]...),
			Schema:                    append([]byte(nil), policy.Schema...),
			Policies:                  append([]byte(nil), policy.Policies...),
			Entities:                  append([]byte(nil), policy.Entities...),
			InstalledBy:               actorID,
			InstallationCorrelationID: correlationID,
			ReasonDigest:              append([]byte(nil), reasonDigest[:]...),
		},
	)
}

func readPolicyFile(path string) ([]byte, error) {
	return readBoundedRegularFile(path, "policy", maximumPolicyFileBytes, false)
}

func readEntityFile(path string) ([]byte, error) {
	return readBoundedRegularFile(path, "entity", maximumEntityFileBytes, true)
}

func readBoundedRegularFile(
	path string,
	kind string,
	maximumBytes int64,
	requireOwnerOnly bool,
) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s file: %w", kind, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect %s file: %w", kind, err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s path: %w", kind, err)
	}
	if !pathInfo.Mode().IsRegular() ||
		!os.SameFile(pathInfo, info) ||
		!info.Mode().IsRegular() ||
		info.Size() <= 0 ||
		info.Size() > maximumBytes ||
		requireOwnerOnly && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf(
			"%s file must be a non-empty owner-only regular file no larger than 1 MiB",
			kind,
		)
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s file: %w", kind, err)
	}
	if len(content) == 0 || int64(len(content)) > maximumBytes {
		return nil, fmt.Errorf(
			"%s file must be a non-empty owner-only regular file no larger than 1 MiB",
			kind,
		)
	}
	return content, nil
}
