package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/asabla/dataground/internal/persistence"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	change, err := parseProviderCredentialGrantChange(arguments)
	if err != nil {
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
	return persistence.NewRepository(pool).ChangeProviderCredentialGrant(operationCtx, change)
}

func parseProviderCredentialGrantChange(arguments []string) (persistence.ProviderCredentialGrantChange, error) {
	flags := flag.NewFlagSet("dataground-provider-credential-grant", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var operation, domainID, revisionID, profile, activatedValue, expiresValue, actorID, reason, correlationID string
	var generation uint64
	flags.StringVar(&operation, "operation", "", "activate or revoke the exact provider credential grant")
	flags.StringVar(&domainID, "isolation-domain", "", "isolation domain identifier")
	flags.StringVar(&revisionID, "revision", "", "service revision identifier")
	flags.StringVar(&profile, "provider-profile", "", "deployment-owned OpenShell provider profile")
	flags.Uint64Var(&generation, "generation", 0, "sequential grant generation")
	flags.StringVar(&activatedValue, "activated-at", "", "activation time in RFC3339")
	flags.StringVar(&expiresValue, "expires-at", "", "expiry time in RFC3339")
	flags.StringVar(&actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&reason, "reason", "", "operator-visible grant reason")
	flags.StringVar(&correlationID, "correlation-id", "", "stable grant correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return persistence.ProviderCredentialGrantChange{}, err
	}
	if flags.NArg() != 0 || operation == "" || domainID == "" || revisionID == "" ||
		profile == "" || generation == 0 || generation > math.MaxInt64 ||
		actorID == "" || !validProviderCredentialGrantReason(reason) || correlationID == "" {
		return persistence.ProviderCredentialGrantChange{}, errors.New("all provider credential grant flags are required")
	}
	change := persistence.ProviderCredentialGrantChange{
		Contract:          persistence.ProviderCredentialGrantContract,
		IsolationDomainID: domainID,
		RevisionID:        revisionID,
		ProviderProfile:   profile,
		Purpose:           persistence.ProviderCredentialPurposeAgentInference,
		Generation:        int64(generation),
		Operation:         operation,
		ActorID:           actorID,
		CorrelationID:     correlationID,
	}
	reasonDigest := sha256.Sum256([]byte(reason))
	change.ReasonDigest = append([]byte(nil), reasonDigest[:]...)
	switch operation {
	case "activate":
		if activatedValue == "" || expiresValue == "" {
			return persistence.ProviderCredentialGrantChange{}, errors.New("activation requires activated-at and expires-at")
		}
		change.ActivatedAt, _ = time.Parse(time.RFC3339Nano, activatedValue)
		change.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresValue)
	case "revoke":
		if activatedValue != "" || expiresValue != "" {
			return persistence.ProviderCredentialGrantChange{}, errors.New("revocation must not set activation times")
		}
	default:
		return persistence.ProviderCredentialGrantChange{}, errors.New("provider credential grant operation is invalid")
	}
	if !change.Valid() {
		return persistence.ProviderCredentialGrantChange{}, persistence.ErrProviderCredentialGrantInvalid
	}
	return change, nil
}

func validProviderCredentialGrantReason(value string) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
