package canarylauncher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCandidateInspectionRejectsImageAndRuntimeSubstitution(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	for _, change := range []string{"none", "image", "root-user", "source", "certification", "missing-labels"} {
		t.Run(change, func(t *testing.T) {
			labels := map[string]string{"dataground.dev.codex-compatibility-source": "4c70bff480af37b1bf1a9b352b8341060fe55755", "dataground.dev.certification-eligible": "false"}
			value := map[string]any{"id": image, "user": "sandbox", "labels": labels}
			switch change {
			case "image":
				value["id"] = "sha256:" + strings.Repeat("b", 64)
			case "root-user":
				value["user"] = "root"
			case "source":
				labels["dataground.dev.codex-compatibility-source"] = strings.Repeat("0", 40)
			case "certification":
				labels["dataground.dev.certification-eligible"] = "true"
			case "missing-labels":
				delete(value, "labels")
			}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if got := validCandidateInspection(data, image); got != (change == "none") {
				t.Fatalf("accepted = %v", got)
			}
		})
	}
	for _, image := range []string{"candidate:latest", "sha256:" + strings.Repeat("a", 63), ""} {
		if err := CheckCandidate(context.Background(), Config{}, image); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("invalid image returned %v", err)
		}
	}
}

func TestCandidateCredentialNonExposure(t *testing.T) {
	image := os.Getenv("DATAGROUND_TEST_CODEX_COMPATIBILITY_IMAGE")
	if image == "" {
		t.Skip("DATAGROUND_TEST_CODEX_COMPATIBILITY_IMAGE selects the synthetic candidate scan")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("candidate credential scan requires Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckCandidate(ctx, Config{
		RepositoryRoot:  os.Getenv("DATAGROUND_TEST_RUNTIME_TOPOLOGY_ROOT"),
		WorkspaceRoot:   workspace,
		OpenShellBinary: os.Getenv("DATAGROUND_TEST_OPENSHELL_BINARY"),
	}, image); err != nil {
		t.Fatalf("candidate credential scan failed at %s", StageOf(err))
	}
}

func TestSupervisorCandidateCannotProduceStockCertification(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	config := Config{supervisorCandidateImage: image}
	if _, err := Run(context.Background(), config); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("candidate topology entered the certifying launcher")
	}
	if err := CheckCandidate(context.Background(), config, image); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatal("candidate topology entered the stock candidate scan")
	}
	for _, selected := range []string{"", "candidate:latest", "sha256:" + strings.Repeat("a", 63)} {
		if err := CheckSupervisorCandidate(context.Background(), Config{}, image, selected); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatal("invalid supervisor identity reached acquisition")
		}
	}
}

func TestCandidateInspectionOutputCannotBypassBound(t *testing.T) {
	var output candidateInspectionOutput
	reader := struct{ io.Reader }{strings.NewReader(strings.Repeat("a", (16<<10)+1))}
	if _, err := io.Copy(&output, reader); err == nil || output.buffer.Len() > 16<<10 {
		t.Fatal("ReaderFrom bypassed the candidate inspection limit")
	}
}

func TestStrictSupervisorCandidateCredentialNonExposure(t *testing.T) {
	supervisor := os.Getenv("DATAGROUND_TEST_SUPERVISOR_COMPATIBILITY_IMAGE")
	if supervisor == "" {
		t.Skip("DATAGROUND_TEST_SUPERVISOR_COMPATIBILITY_IMAGE selects the strict candidate scan")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("candidate credential scan requires Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckSupervisorCandidate(ctx, Config{
		RepositoryRoot: os.Getenv("DATAGROUND_TEST_RUNTIME_TOPOLOGY_ROOT"), WorkspaceRoot: workspace,
		OpenShellBinary: os.Getenv("DATAGROUND_TEST_OPENSHELL_BINARY"),
	}, os.Getenv("DATAGROUND_TEST_CODEX_COMPATIBILITY_IMAGE"), supervisor); err != nil {
		t.Fatalf("strict candidate credential scan failed at %s", StageOf(err))
	}
}
