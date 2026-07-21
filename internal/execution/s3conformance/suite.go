// Package s3conformance verifies the immutable enforcement-object contract
// against a concrete backend. The suite is intentionally destructive within a
// caller-provisioned disposable bucket and has no cleanup authority.
package s3conformance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sync"

	"github.com/asabla/dataground/internal/execution"
)

const (
	ReportSchemaV1          = "dataground.s3-enforcement-conformance/v1"
	defaultConcurrentWrites = 8
)

var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Backend interface {
	execution.EnforcementObjectReader
	execution.EnforcementObjectWriter
}

type Config struct {
	RunID             string
	ConcurrentWriters int
}

type CaseResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Report is safe release evidence. It deliberately excludes endpoint, bucket,
// object keys, policy bytes, credentials, and upstream error details.
type Report struct {
	SchemaVersion string       `json:"schemaVersion"`
	RunID         string       `json:"runId"`
	Status        string       `json:"status"`
	Cases         []CaseResult `json:"cases"`
}

type SuiteError struct {
	Case string
}

func (err *SuiteError) Error() string {
	return "S3 enforcement-object conformance case failed: " + err.Case
}

func Run(ctx context.Context, backend Backend, config Config) (Report, error) {
	report := Report{
		SchemaVersion: ReportSchemaV1,
		Status:        "failed",
		Cases:         make([]CaseResult, 0, 4),
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if backend == nil || !runIDPattern.MatchString(config.RunID) {
		return report, errors.New("invalid S3 conformance configuration")
	}
	report.RunID = config.RunID
	if config.ConcurrentWriters == 0 {
		config.ConcurrentWriters = defaultConcurrentWrites
	}
	if config.ConcurrentWriters < 2 || config.ConcurrentWriters > 32 {
		return report, errors.New("invalid S3 conformance writer count")
	}

	cases := []struct {
		name string
		run  func(context.Context, Backend, Config) error
	}{
		{name: "missing-read", run: verifyMissingRead},
		{name: "create-read", run: verifyCreateAndRead},
		{name: "immutable-rewrite", run: verifyImmutableRewrite},
		{name: "concurrent-create", run: verifyConcurrentCreate},
	}
	for _, candidate := range cases {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := candidate.run(ctx, backend, config); err != nil {
			report.Cases = append(report.Cases, CaseResult{Name: candidate.name, Status: "failed"})
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			return report, &SuiteError{Case: candidate.name}
		}
		report.Cases = append(report.Cases, CaseResult{Name: candidate.name, Status: "passed"})
	}
	report.Status = "passed"
	return report, nil
}

func verifyMissingRead(ctx context.Context, backend Backend, config Config) error {
	object, err := backend.OpenEnforcementObject(ctx, objectKey(config.RunID, "missing"))
	if err == nil {
		if object != nil {
			_ = object.Close()
		}
		return errors.New("unexpected conformance object")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !errors.Is(err, execution.ErrEnforcementObjectMissing) {
		return errors.New("missing object did not use the stable signal")
	}
	return nil
}

func verifyCreateAndRead(ctx context.Context, backend Backend, config Config) error {
	content := conformanceContent("create-read", 0)
	key := objectKey(config.RunID, "create-read")
	if err := put(ctx, backend, key, content); err != nil {
		return err
	}
	observed, err := read(ctx, backend, key)
	if err != nil || !bytes.Equal(observed, content) {
		return errors.New("created object did not read back exactly")
	}
	return nil
}

func verifyImmutableRewrite(ctx context.Context, backend Backend, config Config) error {
	key := objectKey(config.RunID, "immutable-rewrite")
	original := conformanceContent("immutable-rewrite", 0)
	if err := put(ctx, backend, key, original); err != nil {
		return err
	}
	if err := put(ctx, backend, key, conformanceContent("immutable-rewrite", 1)); !errors.Is(err, execution.ErrEnforcementObjectConflict) {
		return errors.New("existing object accepted a replacement")
	}
	observed, err := read(ctx, backend, key)
	if err != nil || !bytes.Equal(observed, original) {
		return errors.New("failed replacement changed immutable content")
	}
	return nil
}

func verifyConcurrentCreate(ctx context.Context, backend Backend, config Config) error {
	key := objectKey(config.RunID, "concurrent-create")
	start := make(chan struct{})
	results := make(chan writeResult, config.ConcurrentWriters)
	var workers sync.WaitGroup
	for candidate := 0; candidate < config.ConcurrentWriters; candidate++ {
		content := conformanceContent("concurrent-create", candidate)
		workers.Add(1)
		go func(owned []byte) {
			defer workers.Done()
			<-start
			results <- writeResult{content: owned, err: put(ctx, backend, key, owned)}
		}(content)
	}
	close(start)
	workers.Wait()
	close(results)

	var winner []byte
	for result := range results {
		if result.err == nil {
			if winner != nil {
				return errors.New("multiple concurrent creates succeeded")
			}
			winner = result.content
			continue
		}
		if !errors.Is(result.err, execution.ErrEnforcementObjectConflict) {
			return errors.New("concurrent create returned an unstable outcome")
		}
	}
	if winner == nil {
		return errors.New("no concurrent create succeeded")
	}
	observed, err := read(ctx, backend, key)
	if err != nil || !bytes.Equal(observed, winner) {
		return errors.New("concurrent winner was not durable")
	}
	return nil
}

type writeResult struct {
	content []byte
	err     error
}

func put(ctx context.Context, backend Backend, key string, content []byte) error {
	digest := sha256.Sum256(content)
	return backend.PutEnforcementObjectIfAbsent(
		ctx,
		key,
		bytes.NewReader(content),
		int64(len(content)),
		"sha256:"+hex.EncodeToString(digest[:]),
	)
}

func read(ctx context.Context, backend Backend, key string) ([]byte, error) {
	object, err := backend.OpenEnforcementObject(ctx, key)
	if err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("conformance backend returned an empty stream")
	}
	content, readErr := io.ReadAll(io.LimitReader(object, execution.MaximumEnforcementPolicyBytes+1))
	closeErr := object.Close()
	if readErr != nil || closeErr != nil || len(content) > execution.MaximumEnforcementPolicyBytes {
		return nil, errors.New("conformance object read failed")
	}
	return content, nil
}

func objectKey(runID string, caseName string) string {
	scope := sha256.Sum256([]byte(runID))
	caseDigest := sha256.Sum256([]byte(caseName))
	return fmt.Sprintf(
		"enforcement-bundles/v1/iso_%s/rev_%s/conformance-%s/%s.yaml",
		hex.EncodeToString(scope[:10]),
		hex.EncodeToString(scope[10:20]),
		caseName,
		hex.EncodeToString(caseDigest[:]),
	)
}

func conformanceContent(caseName string, candidate int) []byte {
	return []byte(fmt.Sprintf(
		"version: 1\nmetadata:\n  purpose: dataground-s3-conformance\n  case: %s\n  candidate: %d\n",
		caseName,
		candidate,
	))
}
