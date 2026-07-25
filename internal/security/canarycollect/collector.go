package canarycollect

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/asabla/dataground/internal/security/canaryscan"
)

var (
	ErrInvalidConfiguration = errors.New("invalid credential source collection configuration")
	ErrAcquisition          = errors.New("credential source acquisition failed")
	ErrScan                 = errors.New("credential source scan failed")
	ErrCanaryDetected       = errors.New("credential canary detected")
	ErrSourceClose          = errors.New("credential source close failed")
)

var surfaceOrder = []string{
	"sandbox-process",
	"sandbox-environment",
	"sandbox-filesystem",
	"provider-arguments",
	"gateway-logs",
	"sandbox-logs",
	"runtime-errors",
}

type ResourceNames struct {
	Gateway string
	Sandbox string
	Provider string
	Runtime string
}

type Source struct {
	Surface string
	Acquire func(context.Context) (io.ReadCloser, error)
}

type Config struct {
	RunID            string
	CanaryCommitment string
	Resources        ResourceNames
	Limits           map[string]int64
	Sources          []Source
}

// Collection is an opaque ordered set of scanner-owned reports. Its JSON form
// is the checks array consumed by the credential evidence contract.
type Collection struct {
	reports []canaryscan.Report
}

// Collect validates the complete seven-source plan before acquisition, then
// opens each source once in canonical order and passes it directly to the
// scanner-owned report boundary.
func Collect(ctx context.Context, config Config) (Collection, error) {
	plans, err := validate(config)
	if err != nil {
		return Collection{}, err
	}

	collection := Collection{reports: make([]canaryscan.Report, 0, len(plans))}
	for _, plan := range plans {
		source, acquireErr := plan.acquire(ctx)
		if acquireErr != nil || source == nil {
			return collection, collectionError(ErrAcquisition, ctx)
		}

		report, scanErr := canaryscan.ScanReport(ctx, source, plan.report)
		closeErr := source.Close()
		collection.reports = append(collection.reports, report)

		var outcome error
		switch {
		case scanErr != nil:
			outcome = ErrScan
		case report.HasMatches():
			outcome = ErrCanaryDetected
		}
		if closeErr != nil {
			outcome = errors.Join(outcome, ErrSourceClose)
		}
		if outcome != nil {
			return collection, collectionError(outcome, ctx)
		}
	}
	return collection, nil
}

func (collection Collection) MarshalJSON() ([]byte, error) {
	return json.Marshal(collection.reports)
}

type sourcePlan struct {
	acquire func(context.Context) (io.ReadCloser, error)
	report  canaryscan.ReportConfig
}

func validate(config Config) ([]sourcePlan, error) {
	if len(config.Sources) != len(surfaceOrder) || len(config.Limits) != len(surfaceOrder) {
		return nil, ErrInvalidConfiguration
	}

	sources := make(map[string]func(context.Context) (io.ReadCloser, error), len(config.Sources))
	for _, source := range config.Sources {
		if source.Acquire == nil {
			return nil, ErrInvalidConfiguration
		}
		if _, exists := sources[source.Surface]; exists {
			return nil, ErrInvalidConfiguration
		}
		sources[source.Surface] = source.Acquire
	}

	plans := make([]sourcePlan, 0, len(surfaceOrder))
	for _, surface := range surfaceOrder {
		acquire, ok := sources[surface]
		limit, hasLimit := config.Limits[surface]
		if !ok || !hasLimit {
			return nil, ErrInvalidConfiguration
		}
		report := canaryscan.ReportConfig{
			Surface:          surface,
			RunID:            config.RunID,
			ResourceName:     resourceName(config.Resources, surface),
			CanaryCommitment: config.CanaryCommitment,
			MaxBytes:         limit,
		}
		if err := canaryscan.ValidateReportConfig(report); err != nil {
			return nil, ErrInvalidConfiguration
		}
		plans = append(plans, sourcePlan{acquire: acquire, report: report})
	}
	return plans, nil
}

func resourceName(resources ResourceNames, surface string) string {
	switch surface {
	case "gateway-logs":
		return resources.Gateway
	case "provider-arguments":
		return resources.Provider
	case "runtime-errors":
		return resources.Runtime
	default:
		return resources.Sandbox
	}
}

func collectionError(outcome error, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(outcome, err)
	}
	return outcome
}
