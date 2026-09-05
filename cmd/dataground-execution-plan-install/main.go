package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/persistence"
)

const maximumPlanBytes = 64 << 10

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	binding, err := readInstallation(arguments)
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
		return errors.New("execution plan database is unavailable")
	}
	if err := persistence.RequireCurrentSchema(operationCtx, database); err != nil {
		database.Close()
		return errors.New("execution plan installation requires the current database schema")
	}
	if err := database.Close(); err != nil {
		return errors.New("execution plan database check failed")
	}
	pool, err := persistence.OpenPool(operationCtx, databaseURL)
	if err != nil {
		return errors.New("execution plan database is unavailable")
	}
	defer pool.Close()
	_, err = executionpostgres.New(pool).BindExecutionPlan(operationCtx, binding)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrExecutionPlanConflict):
			return execution.ErrExecutionPlanConflict
		case errors.Is(err, execution.ErrExecutionPlanRevisionMissing):
			return execution.ErrExecutionPlanRevisionMissing
		case errors.Is(err, execution.ErrExecutionPlanRevisionMismatch):
			return execution.ErrExecutionPlanRevisionMismatch
		default:
			// A lost commit acknowledgement is recoverable by retrying the same
			// binding. Never expose database diagnostics or claim rollback here.
			return errors.New("execution plan installation outcome is unavailable; retry the exact installation")
		}
	}
	return nil
}

func readInstallation(arguments []string) (execution.ExecutionPlanBinding, error) {
	flags := flag.NewFlagSet("dataground-execution-plan-install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var path, digest, domainID, revisionID, actorID, correlationID string
	register := func(name, usage string, target *string) {
		seen := false
		flags.Func(name, usage, func(value string) error {
			if seen {
				return errors.New("flag is repeated")
			}
			seen = true
			*target = value
			return nil
		})
	}
	register("plan-file", "reviewed canonical execution plan file", &path)
	register("plan-digest", "reviewed execution plan sha256 digest", &digest)
	register("isolation-domain", "exact isolation domain identifier", &domainID)
	register("revision", "exact service revision identifier", &revisionID)
	register("actor", "authorized operator identifier", &actorID)
	register("correlation-id", "stable installation correlation identifier", &correlationID)
	if err := flags.Parse(arguments); err != nil {
		return execution.ExecutionPlanBinding{}, errors.New("execution plan installation flags are invalid")
	}
	if flags.NArg() != 0 || flags.NFlag() != 6 || path == "" || digest == "" ||
		domainID == "" || revisionID == "" || actorID == "" || correlationID == "" {
		return execution.ExecutionPlanBinding{}, errors.New("all execution plan installation flags are required")
	}
	content, err := readPlanFile(path)
	if err != nil {
		return execution.ExecutionPlanBinding{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var plan execution.ExecutionPlan
	if err := decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return execution.ExecutionPlanBinding{}, errors.New("execution plan JSON is invalid")
	}
	binding, err := execution.NormalizeExecutionPlanBinding(execution.ExecutionPlanBinding{
		Plan: plan, ActorID: actorID, CorrelationID: correlationID,
	})
	if err != nil {
		return execution.ExecutionPlanBinding{}, errors.New("execution plan binding is invalid")
	}
	canonical, err := json.Marshal(binding.Plan)
	if err != nil || !bytes.Equal(content, append(canonical, '\n')) {
		return execution.ExecutionPlanBinding{}, errors.New("execution plan must use normalized canonical JSON with one trailing newline")
	}
	if binding.Plan.IsolationDomainID != domainID || binding.Plan.RevisionID != revisionID {
		return execution.ExecutionPlanBinding{}, errors.New("execution plan does not match the requested scope")
	}
	actualDigest, err := execution.DigestExecutionPlan(binding.Plan)
	if err != nil || actualDigest != digest {
		return execution.ExecutionPlanBinding{}, errors.New("execution plan does not match the reviewed digest")
	}
	return binding, nil
}

func readPlanFile(path string) ([]byte, error) {
	invalid := errors.New("execution plan file must be a non-empty owner-only regular file no larger than 64 KiB")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 ||
		before.Size() < 1 || before.Size() > maximumPlanBytes {
		return nil, invalid
	}
	// Do not follow a substituted symlink or block on a substituted FIFO.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, invalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() ||
		opened.Mode().Perm()&0o077 != 0 {
		return nil, invalid
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumPlanBytes+1))
	if err != nil || len(content) < 1 || len(content) > maximumPlanBytes {
		return nil, invalid
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) ||
		after.Mode().Perm()&0o077 != 0 {
		return nil, invalid
	}
	return content, nil
}
