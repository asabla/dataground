package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

// The marker is a completed command artifact, not model text. Cancellation must
// reach an active invocation after the copy has completed, before native success.
func cancelLiveOwnedWorkspace(t *testing.T, ctx context.Context, repo *persistence.Repository, worker *reconcile.Reconciler, ready func(context.Context) error, public *liveWorkspaceAPI, invocation domain.Invocation, journey liveWorkspaceJourney) domain.Invocation {
	t.Helper()
	if public == nil {
		t.Fatal("owned cancellation requires the authenticated public fixture")
	}
	recoverExpired := journey == liveWorkspaceRecovery || journey == liveWorkspacePublicRecovery
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	parked, resume := make(chan struct{}), make(chan struct{})
	go func() {
		awaitRecovery := recoverExpired
		for {
			if err := ready(runCtx); err != nil {
				done <- err
				return
			}
			_, runErr := worker.RunOne(runCtx, persistence.OperationKindInvocation)
			value, readErr := repo.GetInvocation(runCtx, invocation.Metadata.IsolationDomainID, invocation.Metadata.ID)
			if readErr != nil {
				done <- readErr
				return
			}
			// Public cancellation intentionally invalidates the executing lease.
			if runErr != nil && !(errors.Is(runErr, persistence.ErrLeaseLost) && value.State == "cancelling") {
				done <- runErr
				return
			}
			if value.CompletedAt != nil {
				done <- nil
				return
			}
			if awaitRecovery && value.State == "cancelling" {
				awaitRecovery = false
				close(parked)
				select {
				case <-resume:
				case <-runCtx.Done():
					done <- runCtx.Err()
					return
				}
			}
			select {
			case <-runCtx.Done():
				done <- runCtx.Err()
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	joined := false
	defer func() {
		stop()
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Minute):
				t.Error("cancelled fixture worker did not stop before cleanup")
			}
		}
	}()
	var after uint64
	marker := false
	for !marker {
		events, err := repo.ListEventsBounded(ctx, invocation.Metadata.IsolationDomainID, invocation.Metadata.ID, after, 100)
		if err != nil {
			t.Fatal("read cancellation readiness events", err)
		}
		for _, event := range events {
			after = event.Sequence
			if event.Type != dgruntime.CommandArtifactEvent {
				continue
			}
			command, err := dgruntime.ParseCommandArtifact(event.Payload)
			if err == nil && command.Status == "completed" && command.ExitCode != nil && *command.ExitCode == 0 && strings.TrimSpace(command.Preview) == "dataground-cancel-ready" {
				marker = true
			}
		}
		if marker {
			break
		}
		select {
		case err := <-done:
			joined = true
			t.Fatal("worker finished before the cancellation marker", err)
		case <-ctx.Done():
			t.Fatal("cancellation marker deadline", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if current := public.invocation(invocation.Metadata.ID); current.State != "running" || current.CompletedAt != nil {
		t.Fatal("native invocation ended before public cancellation")
	}
	public.cancel(invocation.Metadata.ID)
	if recoverExpired {
		select {
		case <-parked:
		case err := <-done:
			joined = true
			t.Fatal("worker ended before retention recovery", err)
		case <-ctx.Done():
			t.Fatal("owner stop deadline", ctx.Err())
		}
		recoverExpiredLiveWorkspace(t, ctx, repo, worker, ready, public, invocation, journey == liveWorkspacePublicRecovery)
		close(resume)
	}
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatal("live cancellation reconciliation", err)
		}
	case <-ctx.Done():
		t.Fatal("live cancellation deadline", ctx.Err())
	}
	return public.invocation(invocation.Metadata.ID)
}

func (live *liveWorkspaceAPI) cancel(id string) {
	live.t.Helper()
	path := live.root() + "/invocations/" + id + "/actions/cancel"
	body := map[string]any{}
	live.call(http.MethodPost, path, body, "live-cancel-unauthenticated", false, http.StatusUnauthorized)
	live.call(http.MethodPost, strings.Replace(path, live.target.Target.IsolationDomainID, identity.New("iso"), 1), body, "live-cancel-foreign", true, http.StatusForbidden)
	accepted := live.call(http.MethodPost, path, body, "live-owned-cancel", true, http.StatusAccepted)
	var value domain.Invocation
	if json.Unmarshal(accepted, &value) != nil || value.Metadata.ID != id || value.State != "cancelling" {
		live.t.Fatal("public cancellation was not accepted while running")
	}
	replayed := live.call(http.MethodPost, path, body, "live-owned-cancel", true, http.StatusAccepted)
	if !bytes.Equal(accepted, replayed) {
		live.t.Fatal("public cancellation replay changed")
	}
}
