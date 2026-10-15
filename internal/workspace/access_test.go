package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type sourceAuthorizerFixture struct {
	phases []string
	permit SourcePermit
	deny   string
	after  func(string)
}

func (a *sourceAuthorizerFixture) AuthorizeWorkspaceSource(_ context.Context, _ SourceUse, phase string) (SourcePermit, error) {
	a.phases = append(a.phases, phase)
	if a.after != nil {
		a.after(phase)
	}
	if a.deny == phase {
		return SourcePermit{}, ErrSourceUnauthorized
	}
	return a.permit, nil
}

type blockedSourceObjects struct{ SourceObjects }

func (o blockedSourceObjects) OpenWorkspaceSource(ctx context.Context, _ string) (io.ReadCloser, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestSourceReadRequiresTwoDecisionsAndExactRetainedBytes(t *testing.T) {
	for _, scenario := range []string{"success", "admission-deny", "delivery-deny", "profile", "content", "extra-byte", "pending", "catalog-drift", "generation", "expired", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			installation, data := sourceInstallation(t)
			catalog := &sourceCatalogFixture{entry: SourceCatalogEntry{Installation: installation, Ready: true}}
			objects := &sourceObjectFixture{content: bytes.Clone(data), profile: installation.Record.StorageProfileDigest}
			auth := &sourceAuthorizerFixture{permit: SourcePermit{Generation: 1, ExpiresAt: time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)}}
			use := SourceUse{Contract: SourceUseV1, Record: installation.Record, RevisionID: "rev_" + strings.Repeat("6", 20), OperationID: "op_" + strings.Repeat("7", 20), GrantGeneration: 1, ActorID: installation.ActorID, CorrelationID: installation.CorrelationID}
			var store SourceObjects = objects
			switch scenario {
			case "admission-deny":
				auth.deny = SourceReadAdmission
			case "delivery-deny":
				auth.deny = SourceReadDelivery
			case "profile":
				objects.profile = "sha256:" + strings.Repeat("8", 64)
			case "content":
				objects.content[0] ^= 1
			case "extra-byte":
				objects.content = append(objects.content, 1)
			case "pending":
				catalog.entry.Ready = false
			case "catalog-drift":
				catalog.entry.Installation.Record.SourceBytes++
			case "generation":
				auth.permit.Generation = 2
			case "expired":
				auth.permit.ExpiresAt = time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
			case "deadline":
				auth.permit.ExpiresAt = time.Now().UTC().Add(100 * time.Millisecond).Truncate(time.Microsecond)
				store = blockedSourceObjects{objects}
			}
			snapshot, err := ReadSource(context.Background(), catalog, store, auth, use)
			if scenario == "success" {
				if err != nil || snapshot == nil || len(auth.phases) != 2 || auth.phases[1] != SourceReadDelivery {
					t.Fatal("verified source not delivered", err)
				}
				if !bytes.Equal(snapshot.entries["private-file"].content, []byte("binary\x00content")) {
					t.Fatal("source bytes changed")
				}
				return
			}
			if err == nil || snapshot != nil {
				t.Fatal("source released after failed verification", err)
			}
			if scenario == "admission-deny" || scenario == "profile" || scenario == "pending" || scenario == "catalog-drift" || scenario == "generation" || scenario == "expired" {
				if objects.reads != 0 {
					t.Fatal("failed admission reached storage")
				}
			}
			if scenario == "delivery-deny" && !errors.Is(err, ErrSourceUnauthorized) {
				t.Fatal(err)
			}
			if scenario == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if objects.writes != 0 {
				t.Fatal("read gained write authority")
			}
		})
	}
}
func TestSourceGrantContractBoundsValidityAndAttribution(t *testing.T) {
	installation, _ := sourceInstallation(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	grant := SourceGrantChange{Contract: SourceGrantV1, Record: installation.Record, RevisionID: "rev_" + strings.Repeat("6", 20), Generation: 1, Operation: "activate", ActivatedAt: now, ExpiresAt: now.Add(time.Hour), ActorID: installation.ActorID, CorrelationID: installation.CorrelationID, ReasonDigest: "sha256:" + strings.Repeat("7", 64)}
	if !grant.Valid() {
		t.Fatal("bounded grant rejected")
	}
	for _, change := range []func(*SourceGrantChange){func(v *SourceGrantChange) { v.ExpiresAt = v.ExpiresAt.Add(time.Microsecond) }, func(v *SourceGrantChange) { v.ActivatedAt = v.ActivatedAt.Add(time.Nanosecond) }, func(v *SourceGrantChange) { v.Generation = 0 }, func(v *SourceGrantChange) { v.Operation = "other" }, func(v *SourceGrantChange) { v.ActorID = "bad\nactor" }, func(v *SourceGrantChange) { v.ReasonDigest = "raw reason" }, func(v *SourceGrantChange) { v.RevisionID = "other" }, func(v *SourceGrantChange) { v.ActivatedAt = v.ActivatedAt.In(time.FixedZone("offset", 3600)) }} {
		v := grant
		change(&v)
		if v.Valid() {
			t.Fatal("invalid grant accepted")
		}
	}
	grant.Operation = "revoke"
	if grant.Valid() {
		t.Fatal("revocation retained validity window")
	}
	grant.ActivatedAt = time.Time{}
	grant.ExpiresAt = time.Time{}
	if !grant.Valid() {
		t.Fatal("revocation rejected")
	}
}
