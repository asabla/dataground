package openshell

import (
	"bytes"
	"encoding/json"
	"io"
)

const SupervisorCandidatePatchSHA256 = "5e97724dd9d9e7fad9abed8a46b9a4d6e06979119998c411daf34b2423056057"

const SupervisorCandidateInspectionFormat = `{"id":{{json .Id}},"os":{{json .Os}},"source":{{json (index .Config.Labels "dataground.dev.supervisor-compatibility-source")}},"patch":{{json (index .Config.Labels "dataground.dev.supervisor-compatibility-patch")}},"certification":{{json (index .Config.Labels "dataground.dev.certification-eligible")}}}`

// VerifySupervisorCandidateInspection binds an explicit local test image. Image
// labels are local operator metadata, not signed publication or runtime proof.
func VerifySupervisorCandidateInspection(output []byte, image string) bool {
	if !localDiagnosticImagePattern.MatchString(image) || len(output) == 0 || len(output) > 4096 {
		return false
	}
	var value struct {
		ID, OS, Source, Patch, Certification string
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&value) == nil && decoder.Decode(new(any)) == io.EOF &&
		value.ID == image && value.OS == "linux" && value.Source == "d556748771c41cbbd4e4dd7cd9030c798afe2b7d" && value.Patch == SupervisorCandidatePatchSHA256 && value.Certification == "false"
}
