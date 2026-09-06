package runtimeevidence

const supervisorDiagnosticSchemaVersion = "dataground.dev.openshell-runtime-diagnostic/v5"

type diagnosticSupervisorCandidate struct {
	Profile      string `json:"profile"`
	SourceCommit string `json:"sourceCommit"`
	PatchSHA256  string `json:"patchSHA256"`
}

func validSupervisorSelection(supervisor, policy, image, model string) bool {
	return validDiagnosticPolicy(policy, image, model) && (supervisor == "" ||
		(commitmentPattern.MatchString(supervisor) && policy == RosettaRuntimePolicyProfile && image != "" && diagnosticModelPattern.MatchString(model)))
}

func validSupervisorEvidence(supervisor, gateway, policy, image, model string) bool {
	return validSupervisorSelection(supervisor, policy, image, model) &&
		((supervisor == "" && gateway == "") || (supervisor != "" && commitmentPattern.MatchString("sha256:"+gateway)))
}
