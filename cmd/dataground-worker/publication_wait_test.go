package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/jackc/pgx/v5/pgxpool"
)

type publicationWaitReader func(context.Context, string, string) (domain.ServiceRevision, error)

func (reader publicationWaitReader) GetServiceRevision(ctx context.Context, scope, id string) (domain.ServiceRevision, error) {
	return reader(ctx, scope, id)
}

func publicationWaitFixture() (runtimeCertificationTarget, domain.ServiceRevision) {
	target := runtimeCertificationTarget{isolationDomainID: "iso_00000000000000000001", serviceID: "svc_00000000000000000001", revisionID: "rev_00000000000000000001"}
	revision := domain.ServiceRevision{Metadata: domain.ResourceMetadata{IsolationDomainID: target.isolationDomainID, ID: target.revisionID}, ServiceID: target.serviceID, RuntimeProfile: reconcile.CodexAppServerRuntimeProfileV1, RequiredCapabilities: []string{reconcile.CodexAppServerRuntimeProfileV1}, State: "draft"}
	return target, revision
}

func TestWorkerPublicationWaitingRequiresExplicitGovernedConfiguration(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"true", "false", "", "1", "TRUE", " false "} {
		values := validGovernedEnvironment()
		values[waitForPublicationEnvironment] = value
		config, err := loadWorkerConfig(mapEnvironment(values))
		if value != "true" && value != "false" {
			if err == nil {
				t.Fatal("ambiguous waiting value admitted", value)
			}
			continue
		}
		if err != nil || config.waitForPublication != (value == "true") {
			t.Fatal("waiting configuration", value, err)
		}
	}
	config, err := loadWorkerConfig(mapEnvironment(validGovernedEnvironment()))
	if err != nil || config.waitForPublication {
		t.Fatal("default governed startup changed", err)
	}
	for _, mode := range []string{"", workerModeReference, "production"} {
		if _, err := loadWorkerConfig(mapEnvironment(map[string]string{"DATAGROUND_WORKER_MODE": mode, waitForPublicationEnvironment: "true"})); err == nil {
			t.Fatal("waiting enabled in unsupported mode", mode)
		}
	}
}

func TestInvocationWorkerWaitsForExactRevisionPublication(t *testing.T) {
	t.Parallel()
	target, revision := publicationWaitFixture()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reads := 0
	reader := publicationWaitReader(func(_ context.Context, scope, id string) (domain.ServiceRevision, error) {
		if scope != target.isolationDomainID || id != target.revisionID {
			t.Fatal("waiting escaped target")
		}
		reads++
		if reads == 3 {
			revision.State = "published"
		}
		return revision, nil
	})
	if err := awaitGovernedRevisionPublication(ctx, reader, target); err != nil || reads != 3 {
		t.Fatal("publication not observed", reads, err)
	}
	// A restarted worker does not wait again once the exact revision is published.
	reads = 3
	if err := awaitGovernedRevisionPublication(ctx, reader, target); err != nil || reads != 4 {
		t.Fatal("published restart waited", reads, err)
	}
}

func TestInvocationPublicationWaitingRejectsDriftAndUnknownState(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"domain", "service", "revision", "profile", "missing capability", "extra capability", "retired", "unknown", "database", "cancelled read"} {
		t.Run(mode, func(t *testing.T) {
			target, revision := publicationWaitFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads := 0
			reader := publicationWaitReader(func(context.Context, string, string) (domain.ServiceRevision, error) {
				reads++
				switch mode {
				case "domain":
					revision.Metadata.IsolationDomainID = "iso_99999999999999999999"
				case "service":
					revision.ServiceID = "svc_99999999999999999999"
				case "revision":
					revision.Metadata.ID = "rev_99999999999999999999"
				case "profile":
					revision.RuntimeProfile = "reference/v1"
				case "missing capability":
					revision.RequiredCapabilities = nil
				case "extra capability":
					revision.RequiredCapabilities = append(revision.RequiredCapabilities, "unknown")
				case "retired":
					revision.State = "retired"
				case "unknown":
					revision.State = "unknown"
				case "database":
					return domain.ServiceRevision{}, errors.New("private database detail")
				case "cancelled read":
					cancel()
					revision.State = "published"
				}
				return revision, nil
			})
			if err := awaitGovernedRevisionPublication(ctx, reader, target); err == nil || strings.Contains(err.Error(), "private") || reads != 1 {
				t.Fatal("unsafe publication state admitted", reads, err)
			}
		})
	}
}

func TestInvocationPublicationWaitStopsBeforeExecutionComposition(t *testing.T) {
	t.Parallel()
	target, revision := publicationWaitFixture()
	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	reader := publicationWaitReader(func(context.Context, string, string) (domain.ServiceRevision, error) {
		reads++
		cancel()
		return revision, nil
	})
	if err := awaitGovernedRevisionPublication(ctx, reader, target); !errors.Is(err, context.Canceled) || reads != 1 {
		t.Fatal("waiting ignored cancellation", reads, err)
	}
	// The closed context must return from the wait before certification parsing,
	// any use of this unconnected pool, or creation of execution resources.
	pool := new(pgxpool.Pool)
	config := workerConfig{mode: workerModeGovernedDevelopment, waitForPublication: true, certification: runtimeCertificationConfig{target: target}}
	driver, resources, err := composeWorkerDriver(ctx, pool, persistence.NewRepository(pool), config)
	if !errors.Is(err, context.Canceled) || driver != nil || resources != nil {
		t.Fatal("waiting created execution resources", err)
	}
}
