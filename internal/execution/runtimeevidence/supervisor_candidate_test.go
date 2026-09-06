package runtimeevidence

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSupervisorCandidateSelectionIsExactAndCredentialFree(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-cross")
	t.Setenv("CODEX_ACCESS_TOKEN", "must-not-cross")
	image := "sha256:" + strings.Repeat("a", 64)
	inspection := `{"id":"` + image + `","os":"linux","source":"d556748771c41cbbd4e4dd7cd9030c798afe2b7d","certification":"false"}`
	gateway := []byte("before\nsupervisor_image = \"" + supervisorImage + "\"\nafter\n")
	runner := &fakeDockerTopologyRunner{results: []dockerTopologyResult{{output: inspection}}}
	value, err := selectRuntimeSupervisorCandidate(DockerTopologyConfig{supervisorCandidateImage: image}, runner, "/docker", gateway)
	if err != nil || !bytes.Equal(value, bytes.Replace(gateway, []byte(supervisorImage), []byte(image), 1)) || !bytes.Contains(gateway, []byte(supervisorImage)) {
		t.Fatal("candidate selection changed other topology data or the source snapshot")
	}
	if len(runner.calls) != 1 || runner.calls[0].binary != "/docker" || !reflect.DeepEqual(runner.calls[0].args[:4], []string{"image", "inspect", image, "--format"}) {
		t.Fatal("candidate selection acquired a different image")
	}
	for _, entry := range runner.calls[0].environment {
		if strings.Contains(entry, "must-not-cross") {
			t.Fatal("model credentials entered Docker inspection")
		}
	}
	for _, output := range []string{
		"", inspection + "{}", strings.Repeat("a", 4097),
		strings.Replace(inspection, image, "sha256:"+strings.Repeat("b", 64), 1),
		strings.Replace(inspection, "linux", "other", 1),
		strings.Replace(inspection, "d556748771c41cbbd4e4dd7cd9030c798afe2b7d", strings.Repeat("0", 40), 1),
		strings.Replace(inspection, `"false"`, `"true"`, 1),
		strings.Replace(inspection, `"certification":"false"`, `"extra":"false"`, 1),
	} {
		runner := &fakeDockerTopologyRunner{results: []dockerTopologyResult{{output: output}}}
		if _, err := selectRuntimeSupervisorCandidate(DockerTopologyConfig{supervisorCandidateImage: image}, runner, "/docker", gateway); !errors.Is(err, ErrDockerTopologyConfiguration) {
			t.Fatal("unverified candidate accepted")
		}
	}
	for _, pair := range []struct{ image, gateway string }{
		{"candidate:latest", string(gateway)}, {image, "missing"}, {image, string(gateway) + supervisorImage},
	} {
		runner := &fakeDockerTopologyRunner{}
		if _, err := selectRuntimeSupervisorCandidate(DockerTopologyConfig{supervisorCandidateImage: pair.image}, runner, "/docker", []byte(pair.gateway)); !errors.Is(err, ErrDockerTopologyConfiguration) || len(runner.calls) != 0 {
			t.Fatal("invalid selection reached Docker")
		}
	}
}
