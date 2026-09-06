package runtimeevidence

const (
	RosettaRuntimePolicyProfile    = "rosetta-development/v1"
	rosettaRuntimePolicyPath       = "deploy/openshell/codex-compatibility/rosetta-runtime-policy.yaml"
	rosettaRuntimePolicySHA256     = "a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39"
	rosettaRuntimeInputSHA256      = "b2895b9172c50ba7a5fdf574cebdf6789258cc8ce9f90ce5ad8f2b1ff0a825ab"
	rosettaRuntimeSourceCommit     = "320158f1e4a4eea378d82c1527f4a7af5fb9855b"
	rosettaDiagnosticSchemaVersion = "dataground.dev.openshell-runtime-diagnostic/v4"
)

func validDiagnosticPolicy(profile, image, model string) bool {
	return validCandidateSelection(image, model) && (profile == "" ||
		(profile == RosettaRuntimePolicyProfile && image != "" && diagnosticModelPattern.MatchString(model)))
}

func diagnosticPolicyDigest(profile string) string {
	if profile == RosettaRuntimePolicyProfile {
		return rosettaRuntimePolicySHA256
	}
	return candidateRuntimePolicySHA256
}

type diagnosticPolicySource struct {
	Profile              string `json:"profile"`
	CompilerSourceCommit string `json:"compilerSourceCommit"`
	InputSHA256          string `json:"inputSHA256"`
}
