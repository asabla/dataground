package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/execution/recoveryconformance"
	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
)

const commitLossExitCode = 86

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("dataground-enforcement-recovery-conformance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", "", "S3 origin without credentials, path, query, or fragment")
	bucket := flags.String("bucket", "", "caller-provisioned disposable bucket")
	style := flags.String("addressing-style", string(s3store.PathStyle), "path or virtual-hosted")
	runID := flags.String("run-id", "", "unique 32-character lowercase hexadecimal run identifier")
	phase := flags.String("phase", "", "prepare, outage, recover, commit-loss or committed-recover")
	allowLoopbackHTTP := flags.Bool("allow-loopback-http", false, "allow explicit plaintext loopback development endpoint")
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(stderr, "invalid enforcement recovery conformance command configuration")
		return 2
	}
	selectedPhase := recoveryconformance.Phase(*phase)
	if flags.NArg() != 0 || *endpoint == "" || *bucket == "" ||
		*runID == "" || databaseURL == "" || !*allowLoopbackHTTP || !isHTTPStyleLoopback(*endpoint) ||
		!isLoopbackPostgresURL(databaseURL) || invalidRunID(*runID) ||
		!validPhase(selectedPhase) {
		fmt.Fprintln(stderr, "invalid enforcement recovery conformance command configuration")
		return 2
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	backend, err := s3store.New(s3store.Config{
		Endpoint:             *endpoint,
		Bucket:               *bucket,
		AddressingStyle:      s3store.AddressingStyle(*style),
		AllowHTTPForLoopback: *allowLoopbackHTTP,
		HTTPClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "invalid enforcement recovery storage configuration")
		return 2
	}

	report, runErr := execute(
		ctx,
		databaseURL,
		backend,
		recoveryconformance.Config{RunID: *runID},
		selectedPhase,
		func() { os.Exit(commitLossExitCode) },
	)
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, "could not encode enforcement recovery conformance report")
		return 1
	}
	if runErr != nil {
		fmt.Fprintln(stderr, "enforcement recovery conformance failed")
		return 1
	}
	return 0
}

func execute(
	ctx context.Context,
	databaseURL string,
	backend recoveryconformance.Backend,
	config recoveryconformance.Config,
	phase recoveryconformance.Phase,
	terminate func(),
) (recoveryconformance.Report, error) {
	if _, err := recoveryconformance.FixtureFor(config); err != nil {
		return recoveryconformance.Report{}, err
	}
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		return recoveryconformance.Report{}, errors.New("open recovery conformance database")
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
		database.Close()
		return recoveryconformance.Report{}, errors.New("require recovery conformance schema")
	}
	if err := database.Close(); err != nil {
		return recoveryconformance.Report{}, errors.New("close recovery conformance schema connection")
	}
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		return recoveryconformance.Report{}, errors.New("open recovery conformance database pool")
	}
	defer pool.Close()
	catalog := &durableCatalog{Store: postgres.New(pool), pool: pool}

	switch phase {
	case recoveryconformance.PhasePrepare:
		return recoveryconformance.RunPrepare(ctx, catalog, backend, func(ctx context.Context, fixture recoveryconformance.Fixture) error {
			if err := provisionFixture(ctx, pool, fixture); err != nil {
				return errors.New("provision recovery conformance fixture")
			}
			return nil
		}, pool.Close, config)
	case recoveryconformance.PhaseOutage:
		return recoveryconformance.RunOutage(ctx, catalog, backend, config)
	case recoveryconformance.PhaseRecover:
		return recoveryconformance.RunRecover(ctx, catalog, backend, config)
	case recoveryconformance.PhaseCommitLoss:
		return recoveryconformance.RunCommitLoss(ctx, catalog, backend, terminate, config)
	case recoveryconformance.PhaseCommittedRecover:
		return recoveryconformance.RunCommittedRecover(ctx, catalog, backend, config)
	default:
		return recoveryconformance.Report{}, errors.New("invalid recovery conformance phase")
	}
}

func validPhase(phase recoveryconformance.Phase) bool {
	return phase == recoveryconformance.PhasePrepare || phase == recoveryconformance.PhaseOutage ||
		phase == recoveryconformance.PhaseRecover || phase == recoveryconformance.PhaseCommitLoss ||
		phase == recoveryconformance.PhaseCommittedRecover
}

type durableCatalog struct {
	*postgres.Store
	pool *pgxpool.Pool
}

func (catalog *durableCatalog) CountEnforcementBundleBindingAudits(
	ctx context.Context,
	isolationDomainID string,
	bundleID string,
) (int, error) {
	var count int
	err := catalog.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM audit_records
		WHERE isolation_domain_id = $1
		  AND resource_type = 'enforcement-bundle'
		  AND resource_id = $2
		  AND action = 'enforcement-bundle.bind'
	`, isolationDomainID, bundleID).Scan(&count)
	return count, err
}

func provisionFixture(ctx context.Context, pool *pgxpool.Pool, fixture recoveryconformance.Fixture) error {
	transaction, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer transaction.Rollback(ctx)
	now := time.Now().UTC()
	if _, err := transaction.Exec(ctx, `
		INSERT INTO agent_services (
			isolation_domain_id, id, name, description, created_at, updated_at, created_by
		) VALUES ($1, $2, 'enforcement recovery conformance', '', $3, $3, 'conformance:enforcement-recovery')
		ON CONFLICT (isolation_domain_id, id) DO NOTHING
	`, fixture.IsolationDomainID, fixture.ServiceID, now); err != nil {
		return err
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO service_revisions (
			isolation_domain_id, id, service_id, revision_number, state,
			runtime_profile, required_capabilities, created_at, updated_at, created_by
		) VALUES ($1, $2, $3, 1, 'draft', 'codex.app-server/v1',
		          ARRAY['runtime.codex'], $4, $4, 'conformance:enforcement-recovery')
		ON CONFLICT (isolation_domain_id, id) DO NOTHING
	`, fixture.IsolationDomainID, fixture.RevisionID, fixture.ServiceID, now); err != nil {
		return err
	}
	var serviceName, serviceCreator, serviceID, revisionState, runtimeProfile, revisionCreator string
	var revisionNumber int
	var requiredCapabilities []string
	if err := transaction.QueryRow(ctx, `
		SELECT service.name, service.created_by, revision.service_id, revision.revision_number,
		       revision.state, revision.runtime_profile, revision.required_capabilities, revision.created_by
		FROM service_revisions AS revision
		JOIN agent_services AS service
		  ON service.isolation_domain_id = revision.isolation_domain_id
		 AND service.id = revision.service_id
		WHERE revision.isolation_domain_id = $1 AND revision.id = $2
	`, fixture.IsolationDomainID, fixture.RevisionID).Scan(
		&serviceName,
		&serviceCreator,
		&serviceID,
		&revisionNumber,
		&revisionState,
		&runtimeProfile,
		&requiredCapabilities,
		&revisionCreator,
	); err != nil {
		return err
	}
	if serviceName != "enforcement recovery conformance" || serviceCreator != "conformance:enforcement-recovery" ||
		serviceID != fixture.ServiceID || revisionNumber != 1 || revisionState != "draft" ||
		runtimeProfile != "codex.app-server/v1" || len(requiredCapabilities) != 1 ||
		requiredCapabilities[0] != "runtime.codex" || revisionCreator != "conformance:enforcement-recovery" {
		return errors.New("recovery conformance fixture conflicts with persisted revision")
	}
	return transaction.Commit(ctx)
}

func isHTTPStyleLoopback(raw string) bool {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" {
		return false
	}
	address := net.ParseIP(endpoint.Hostname())
	return address != nil && address.IsLoopback()
}

func isLoopbackPostgresURL(raw string) bool {
	databaseURL, err := url.Parse(raw)
	if err != nil || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") ||
		databaseURL.Fragment != "" {
		return false
	}
	address := net.ParseIP(databaseURL.Hostname())
	query := databaseURL.Query()
	return address != nil && address.IsLoopback() && query.Get("sslmode") == "disable" && len(query) == 1
}

func invalidRunID(runID string) bool {
	_, err := recoveryconformance.FixtureFor(recoveryconformance.Config{RunID: runID})
	return err != nil
}

var _ recoveryconformance.Catalog = (*durableCatalog)(nil)
