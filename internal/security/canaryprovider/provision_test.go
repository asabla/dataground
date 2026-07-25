package canaryprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

func TestProvisionOwnsStructuredCanaryAndExactBinding(t *testing.T) {
	t.Parallel()

	entropy := make([]byte, canaryEntropyBytes)
	for index := range entropy {
		entropy[index] = byte(index)
	}
	creator := &fakeProvisioner{binding: testBinding()}
	provisioned, err := provisionWithEntropy(
		context.Background(),
		validProvisionConfig(),
		creator,
		bytes.NewReader(entropy),
	)
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}

	expectedCanary := canaryPrefix + base64.RawURLEncoding.EncodeToString(entropy)
	if string(creator.observedCanary) != expectedCanary {
		t.Fatalf("created canary = %q", creator.observedCanary)
	}
	if strings.Contains(strings.Join([]string{
		creator.request.IsolationDomainID,
		creator.request.GatewayID,
		creator.request.Name,
	}, " "), expectedCanary) {
		t.Fatal("canary entered provider identity fields")
	}
	for _, value := range creator.retainedCanary {
		if value != 0 {
			t.Fatal("provider request retained canary plaintext")
		}
	}

	digest := sha256.Sum256([]byte(expectedCanary))
	expectedCommitment := "sha256:" + hex.EncodeToString(digest[:])
	if provisioned.Commitment() != expectedCommitment {
		t.Fatalf("Commitment() = %q", provisioned.Commitment())
	}
	if provisioned.Binding() != testBinding() {
		t.Fatalf("Binding() = %+v", provisioned.Binding())
	}
}

func TestProvisionRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	valid := validProvisionConfig()
	for name, mutate := range map[string]func(*ProvisionConfig){
		"run": func(config *ProvisionConfig) {
			config.RunID = "invalid"
		},
		"isolation": func(config *ProvisionConfig) {
			config.IsolationDomainID = ""
		},
		"gateway": func(config *ProvisionConfig) {
			config.GatewayID = ""
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config := valid
			mutate(&config)
			if _, err := provisionWithEntropy(
				context.Background(),
				config,
				&fakeProvisioner{},
				bytes.NewReader(make([]byte, canaryEntropyBytes)),
			); !errors.Is(err, ErrInvalidProvisioning) {
				t.Fatalf("Provision() error = %v", err)
			}
		})
	}

	var typedNil *fakeProvisioner
	if _, err := provisionWithEntropy(
		context.Background(),
		valid,
		typedNil,
		bytes.NewReader(make([]byte, canaryEntropyBytes)),
	); !errors.Is(err, ErrInvalidProvisioning) {
		t.Fatalf("Provision(typed nil) error = %v", err)
	}
	if _, err := provisionWithEntropy(
		context.Background(),
		valid,
		&fakeProvisioner{},
		nil,
	); !errors.Is(err, ErrInvalidProvisioning) {
		t.Fatalf("Provision(nil entropy) error = %v", err)
	}
}

func TestProvisionSanitizesEntropyAndProviderFailures(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		entropy io.Reader
		creator *fakeProvisioner
	}{
		"entropy": {
			entropy: errReader{err: errors.New("sensitive entropy payload")},
			creator: &fakeProvisioner{},
		},
		"provider": {
			entropy: bytes.NewReader(make([]byte, canaryEntropyBytes)),
			creator: &fakeProvisioner{err: errors.New("sensitive provider payload")},
		},
	} {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := provisionWithEntropy(
				context.Background(),
				validProvisionConfig(),
				testCase.creator,
				testCase.entropy,
			)
			if !errors.Is(err, ErrProvisioning) {
				t.Fatalf("Provision() error = %v", err)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("Provision() leaked upstream details: %v", err)
			}
		})
	}
}

func TestProvisionPropagatesCancellationWithoutCreating(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	creator := &fakeProvisioner{}
	_, err := provisionWithEntropy(
		ctx,
		validProvisionConfig(),
		creator,
		bytes.NewReader(make([]byte, canaryEntropyBytes)),
	)
	if !errors.Is(err, ErrProvisioning) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Provision() error = %v", err)
	}
	if creator.calls != 0 {
		t.Fatalf("cancelled provision reached provider %d times", creator.calls)
	}
}

func TestProvisionRejectsReturnedBindingDrift(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*execution.ProviderBinding){
		"isolation": func(binding *execution.ProviderBinding) {
			binding.IsolationDomainID = "other"
		},
		"gateway": func(binding *execution.ProviderBinding) {
			binding.GatewayID = "other"
		},
		"name": func(binding *execution.ProviderBinding) {
			binding.Name = "other"
		},
		"id": func(binding *execution.ProviderBinding) {
			binding.ID = ""
		},
		"version": func(binding *execution.ProviderBinding) {
			binding.ResourceVersion = 0
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			binding := testBinding()
			mutate(&binding)
			_, err := provisionWithEntropy(
				context.Background(),
				validProvisionConfig(),
				&fakeProvisioner{binding: binding},
				bytes.NewReader(make([]byte, canaryEntropyBytes)),
			)
			if !errors.Is(err, ErrProvisioning) {
				t.Fatalf("Provision() error = %v", err)
			}
		})
	}
}

func TestProvisionedRefusesSerializationAcrossCopies(t *testing.T) {
	t.Parallel()

	provisioned, err := provisionWithEntropy(
		context.Background(),
		validProvisionConfig(),
		&fakeProvisioner{binding: testBinding()},
		bytes.NewReader(make([]byte, canaryEntropyBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	copied := *provisioned
	if _, err := json.Marshal(copied); !errors.Is(err, ErrProvisionSerialization) {
		t.Fatalf("json.Marshal() error = %v", err)
	}
}

type fakeProvisioner struct {
	binding        execution.ProviderBinding
	err            error
	calls          int
	request        execution.CredentialEvidenceProviderRequest
	observedCanary []byte
	retainedCanary []byte
}

func (provisioner *fakeProvisioner) CreateCredentialEvidenceProvider(
	_ context.Context,
	request execution.CredentialEvidenceProviderRequest,
) (execution.ProviderBinding, error) {
	provisioner.calls++
	provisioner.request = request
	provisioner.observedCanary = append([]byte(nil), request.Canary...)
	provisioner.retainedCanary = request.Canary
	return provisioner.binding, provisioner.err
}

type errReader struct {
	err error
}

func (reader errReader) Read([]byte) (int, error) {
	return 0, reader.err
}

func validProvisionConfig() ProvisionConfig {
	return ProvisionConfig{
		RunID:             testRunID,
		IsolationDomainID: "iso-a",
		GatewayID:         "gateway-a",
	}
}
