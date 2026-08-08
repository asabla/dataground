package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

type policyWithdrawalRepository interface {
	WithdrawInvocationAuthorizationPolicy(
		context.Context,
		persistence.InvocationAuthorizationPolicyWithdrawal,
	) error
}

type commandRequest struct {
	isolationDomainID string
	serviceID         string
	revisionID        string
	policyDigest      []byte
	actorID           string
	reason            string
	correlationID     string
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DataGround invocation authorization policy withdrawal failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	request, err := parseArguments(arguments)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("policy withdrawal context is required")
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
	return executeRequest(
		operationCtx,
		persistence.NewRepository(pool),
		newPolicyWithdrawal(request),
	)
}

func parseArguments(arguments []string) (commandRequest, error) {
	var request commandRequest
	var policyDigest string
	flags := flag.NewFlagSet("dataground-policy-withdraw", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&request.isolationDomainID, "isolation-domain", "", "exact isolation domain")
	flags.StringVar(&request.serviceID, "service", "", "exact agent service")
	flags.StringVar(&request.revisionID, "revision", "", "exact service revision")
	flags.StringVar(&policyDigest, "policy-digest", "", "expected sha256 policy digest")
	flags.StringVar(&request.actorID, "actor", "", "authorized emergency revoker")
	flags.StringVar(&request.reason, "reason", "", "operator-visible withdrawal reason")
	flags.StringVar(&request.correlationID, "correlation-id", "", "stable withdrawal correlation identifier")
	if err := flags.Parse(arguments); err != nil {
		return commandRequest{}, err
	}
	if flags.NArg() != 0 ||
		request.isolationDomainID == "" ||
		request.serviceID == "" ||
		request.revisionID == "" ||
		policyDigest == "" ||
		request.actorID == "" ||
		request.reason == "" ||
		request.correlationID == "" {
		return commandRequest{}, errors.New("all policy withdrawal flags are required")
	}
	decoded, err := parsePolicyDigest(policyDigest)
	if err != nil {
		return commandRequest{}, err
	}
	request.policyDigest = decoded
	return request, nil
}

func parsePolicyDigest(value string) ([]byte, error) {
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

func newPolicyWithdrawal(
	request commandRequest,
) persistence.InvocationAuthorizationPolicyWithdrawal {
	reasonDigest := sha256.Sum256([]byte(request.reason))
	return persistence.InvocationAuthorizationPolicyWithdrawal{
		Contract:          persistence.InvocationAuthorizationPolicyWithdrawalContract,
		IsolationDomainID: request.isolationDomainID,
		ServiceID:         request.serviceID,
		RevisionID:        request.revisionID,
		PolicyDigest:      append([]byte(nil), request.policyDigest...),
		WithdrawnBy:       request.actorID,
		ReasonDigest:      reasonDigest[:],
		CorrelationID:     request.correlationID,
	}
}

func executeRequest(
	ctx context.Context,
	repository policyWithdrawalRepository,
	withdrawal persistence.InvocationAuthorizationPolicyWithdrawal,
) error {
	if repository == nil {
		return errors.New("policy withdrawal repository is required")
	}
	if ctx == nil {
		return errors.New("policy withdrawal context is required")
	}
	return repository.WithdrawInvocationAuthorizationPolicy(ctx, withdrawal)
}
