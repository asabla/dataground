package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

type developmentProviderFake struct {
	id                                              string
	version                                         uint64
	exists, enabled, loseAck, noEffect, unavailable bool
	creates, loads, enables                         int
	retained                                        execution.RuntimeConformanceCredentials
	onCreate                                        func()
}

func (*developmentProviderFake) Check(context.Context) error { return nil }
func (p *developmentProviderFake) EnableProviderProfiles(context.Context, string, string) error {
	p.enables++
	p.enabled = true
	return nil
}
func (p *developmentProviderFake) CheckDevelopmentProviderProfiles(context.Context, string, string) error {
	if !p.enabled {
		return errors.New("private disabled setting")
	}
	return nil
}
func (p *developmentProviderFake) CreateDevelopmentProvider(_ context.Context, iso, gateway string, credentials execution.RuntimeConformanceCredentials) (execution.ProviderBinding, error) {
	p.creates++
	p.retained = credentials
	if !p.noEffect {
		p.exists = true
	}
	if p.onCreate != nil {
		p.onCreate()
	}
	if p.loseAck || p.noEffect {
		return execution.ProviderBinding{}, errors.New("private acknowledgement")
	}
	return execution.ProviderBinding{IsolationDomainID: iso, GatewayID: gateway, Name: "codex", ID: p.id, ResourceVersion: p.version}, nil
}
func (p *developmentProviderFake) ObserveDevelopmentProvider(_ context.Context, iso, gateway string) (execution.ProviderBindingObservation, error) {
	if p.unavailable {
		return execution.ProviderBindingObservation{}, errors.New("private observation")
	}
	v := execution.ProviderBindingObservation{IsolationDomainID: iso, GatewayID: gateway, Name: "codex", Exists: p.exists, ObservedAt: time.Now().UTC()}
	if p.exists {
		v.ID, v.ResourceVersion = p.id, p.version
	}
	return v, nil
}
func developmentProviderFixture(t *testing.T) (DevelopmentProviderConfig, developmentProviderDependencies, *developmentProviderFake, *developmentGatewayFake) {
	t.Helper()
	gateway, gatewayDeps, runner := developmentGatewayFixture(t)
	if _, err := runDevelopmentGateway(context.Background(), "up", gateway, gatewayDeps); err != nil {
		t.Fatal(err)
	}
	port := &developmentProviderFake{id: "private-provider-binding", version: 1}
	config := DevelopmentProviderConfig{Gateway: gateway, OpenShellBinary: "/usr/bin/openshell", CredentialDirectory: "/private/credential-bundle", ActorID: "operator", CorrelationID: "cor_00000000000000000001"}
	deps := developmentProviderDependencies{gateway: gatewayDeps, open: func(context.Context, DevelopmentProviderConfig) (developmentProviderPort, error) { return port, nil }, load: func(context.Context, string) (execution.RuntimeConformanceCredentials, error) {
		port.loads++
		return testRuntimeProviderCredentials(), nil
	}}
	return config, deps, port, runner
}

func TestDevelopmentProviderInstallsOnceAndRetainsExactBinding(t *testing.T) {
	config, deps, port, _ := developmentProviderFixture(t)
	port.loseAck = true
	first, err := runDevelopmentProvider(context.Background(), "install", config, deps)
	if err != nil || first.State != "configured" || first.CertificationEligible {
		t.Fatal(first, err)
	}
	for _, action := range []string{"install", "inspect", "install"} {
		replay, err := runDevelopmentProvider(context.Background(), action, config, deps)
		if err != nil || replay != first || port.loads != 1 || port.creates != 1 || port.enables != 1 {
			t.Fatal("replay mutated provider", err)
		}
	}
	for _, value := range [][]byte{port.retained.AccessToken, port.retained.RefreshToken, port.retained.AccountID, port.retained.IDToken} {
		if !runtimeProviderBytesCleared(value) {
			t.Fatal("credentials retained")
		}
	}
	content, _ := json.Marshal(first)
	if strings.Contains(string(content), port.id) || strings.Contains(string(content), "/private") {
		t.Fatal("private metadata emitted")
	}
	files, _ := filepath.Glob(filepath.Join(config.Gateway.WorkspaceRoot, "*.provider.*"))
	for _, path := range files {
		value, err := os.ReadFile(path)
		info, statErr := os.Stat(path)
		if err != nil || statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("unsafe record")
		}
		for _, secret := range [][]byte{testRuntimeProviderCredentials().AccessToken, testRuntimeProviderCredentials().RefreshToken, testRuntimeProviderCredentials().AccountID, testRuntimeProviderCredentials().IDToken} {
			if strings.Contains(string(value), string(secret)) {
				t.Fatal("credential persisted")
			}
		}
	}
}

func TestDevelopmentProviderRecoversOnlyByObservationAfterIntent(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "present"}[exists], func(t *testing.T) {
			config, deps, port, _ := developmentProviderFixture(t)
			port.onCreate = func() { port.unavailable = true }
			if _, err := runDevelopmentProvider(context.Background(), "install", config, deps); !errors.Is(err, ErrDevelopmentProvider) {
				t.Fatal(err)
			}
			port.unavailable, port.exists = false, exists
			receipt, err := runDevelopmentProvider(context.Background(), "inspect", config, deps)
			if exists && (err != nil || receipt.State != "configured") {
				t.Fatal(err)
			}
			if !exists && err == nil {
				t.Fatal("absent effect accepted")
			}
			if port.creates != 1 || port.loads != 1 {
				t.Fatal("native effect repeated")
			}
		})
	}
}

func TestDevelopmentProviderDeniesSubstitutionAndRetiredGateway(t *testing.T) {
	for name, mutate := range map[string]func(*DevelopmentProviderConfig, *developmentProviderFake, *developmentGatewayFake){
		"scope": func(c *DevelopmentProviderConfig, _ *developmentProviderFake, _ *developmentGatewayFake) {
			c.Gateway.RevisionID = "rev_00000000000000000002"
		},
		"source": func(c *DevelopmentProviderConfig, _ *developmentProviderFake, _ *developmentGatewayFake) {
			c.CredentialDirectory += "-changed"
		},
		"actor": func(c *DevelopmentProviderConfig, _ *developmentProviderFake, _ *developmentGatewayFake) {
			c.ActorID = "changed"
		},
		"correlation": func(c *DevelopmentProviderConfig, _ *developmentProviderFake, _ *developmentGatewayFake) {
			c.CorrelationID = "cor_00000000000000000002"
		},
		"binding": func(_ *DevelopmentProviderConfig, p *developmentProviderFake, _ *developmentGatewayFake) {
			p.id = "replacement"
		},
		"version": func(_ *DevelopmentProviderConfig, p *developmentProviderFake, _ *developmentGatewayFake) { p.version++ },
		"deleted": func(_ *DevelopmentProviderConfig, p *developmentProviderFake, _ *developmentGatewayFake) {
			p.exists = false
		},
		"setting": func(_ *DevelopmentProviderConfig, p *developmentProviderFake, _ *developmentGatewayFake) {
			p.enabled = false
		},
		"restarted gateway": func(_ *DevelopmentProviderConfig, _ *developmentProviderFake, g *developmentGatewayFake) {
			g.started = "2026-01-02T00:00:00Z"
		},
		"stopped gateway": func(_ *DevelopmentProviderConfig, _ *developmentProviderFake, g *developmentGatewayFake) {
			g.running = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			config, deps, port, gateway := developmentProviderFixture(t)
			if _, err := runDevelopmentProvider(context.Background(), "install", config, deps); err != nil {
				t.Fatal(err)
			}
			mutate(&config, port, gateway)
			if _, err := runDevelopmentProvider(context.Background(), "install", config, deps); !errors.Is(err, ErrDevelopmentProvider) {
				t.Fatal("substitution accepted", err)
			}
			if port.creates != 1 || port.loads != 1 || port.enables != 1 {
				t.Fatal("substitution triggered mutation")
			}
		})
	}
}

func TestDevelopmentProviderRejectsExistingAndSerializesGatewayStop(t *testing.T) {
	config, deps, port, _ := developmentProviderFixture(t)
	port.exists = true
	if _, err := runDevelopmentProvider(context.Background(), "install", config, deps); !errors.Is(err, ErrDevelopmentProvider) || port.loads != 0 || port.enables != 0 {
		t.Fatal("preexisting provider adopted")
	}
	port.exists = false
	port.onCreate = func() {
		if _, err := runDevelopmentGateway(context.Background(), "stop", config.Gateway, deps.gateway); err == nil {
			t.Fatal("stop bypassed shared lock")
		}
	}
	if _, err := runDevelopmentProvider(context.Background(), "install", config, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := runDevelopmentGateway(context.Background(), "stop", config.Gateway, deps.gateway); err != nil {
		t.Fatal(err)
	}
	if _, err := runDevelopmentProvider(context.Background(), "inspect", config, deps); err == nil {
		t.Fatal("retired deployment accepted")
	}
}

func TestDevelopmentProviderCancellationDoesNotRepeatCredentialTransfer(t *testing.T) {
	config, deps, port, _ := developmentProviderFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	port.onCreate = cancel
	if _, err := runDevelopmentProvider(ctx, "install", config, deps); !errors.Is(err, ErrDevelopmentProvider) {
		t.Fatal(err)
	}
	if _, err := runDevelopmentProvider(context.Background(), "install", config, deps); err != nil {
		t.Fatal(err)
	}
	if port.creates != 1 || port.loads != 1 {
		t.Fatal("cancelled effect repeated")
	}
}

func TestDevelopmentProviderConsumesRealBundleAndRecoversReceiptFailure(t *testing.T) {
	config, deps, port, _ := developmentProviderFixture(t)
	config.CredentialDirectory = writeRuntimeCredentialBundle(t)
	deps.load = loadDevelopmentProviderCredentials
	receiptPath := filepath.Join(config.Gateway.WorkspaceRoot, "dg-runtime-deployment-"+config.Gateway.RunID+".provider.receipt.json")
	port.onCreate = func() {
		if _, err := os.Stat(config.CredentialDirectory); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("source not consumed before effect")
		}
		if err := os.Mkdir(receiptPath, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runDevelopmentProvider(context.Background(), "install", config, deps); !errors.Is(err, ErrDevelopmentProvider) {
		t.Fatal("receipt failure hidden", err)
	}
	if err := os.Remove(receiptPath); err != nil {
		t.Fatal(err)
	}
	if _, err := runDevelopmentProvider(context.Background(), "inspect", config, deps); err != nil || port.creates != 1 {
		t.Fatal("receipt failure repeated transfer", err)
	}
}

func TestDevelopmentProviderDeniesInvalidRequestBeforeAcquisition(t *testing.T) {
	for name, mutate := range map[string]func(*DevelopmentProviderConfig){
		"actor":             func(c *DevelopmentProviderConfig) { c.ActorID = "bad\nactor" },
		"correlation":       func(c *DevelopmentProviderConfig) { c.CorrelationID = "bad" },
		"binary":            func(c *DevelopmentProviderConfig) { c.OpenShellBinary = "openshell" },
		"repository source": func(c *DevelopmentProviderConfig) { c.CredentialDirectory = c.Gateway.RepositoryRoot + "/secrets" },
		"workspace source":  func(c *DevelopmentProviderConfig) { c.CredentialDirectory = c.Gateway.WorkspaceRoot + "/secrets" },
		"parent source":     func(c *DevelopmentProviderConfig) { c.CredentialDirectory = filepath.Dir(c.Gateway.WorkspaceRoot) },
	} {
		t.Run(name, func(t *testing.T) {
			config, deps, port, _ := developmentProviderFixture(t)
			mutate(&config)
			if _, err := runDevelopmentProvider(context.Background(), "install", config, deps); !errors.Is(err, ErrDevelopmentProvider) || port.loads != 0 || port.creates != 0 || port.enables != 0 {
				t.Fatal("invalid request reached provider", err)
			}
		})
	}
}
