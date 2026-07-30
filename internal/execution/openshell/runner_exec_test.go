package openshell

import (
	"context"
	"reflect"
	"testing"
)

func TestExecRunnerOwnsExplicitEnvironment(t *testing.T) {
	environment := []string{"PATH=/trusted/bin", "HOME=/private"}
	runner := ExecRunner{Environment: environment}
	command := runner.command(context.Background(), "/trusted/bin/openshell", "--version")
	environment[0] = "PATH=/mutated"

	if !reflect.DeepEqual(command.Env, []string{"PATH=/trusted/bin", "HOME=/private"}) {
		t.Fatalf("command environment = %#v", command.Env)
	}
	if !reflect.DeepEqual(command.Args, []string{"/trusted/bin/openshell", "--version"}) {
		t.Fatalf("command arguments = %#v", command.Args)
	}
}

func TestClassifyNativeFailureSanitizesSessionOutcomes(t *testing.T) {
	cases := []struct {
		message string
		want    NativeFailureClass
	}{
		{"status: Internal", NativeFailureDriver},
		{"status: FailedPrecondition", NativeFailureDriver},
		{"WebSocket handshake failed", NativeFailureSupervisor},
		{"ssh exited with status exit status 255", NativeFailureSupervisor},
		{"command failed", NativeFailureSupervisor},
		{"status: Unavailable", NativeFailureNetwork},
		{"transport error", NativeFailureNetwork},
		{"deadline exceeded", NativeFailureTimeout},
		{"status: Cancelled", NativeFailureTimeout},
	}
	for _, test := range cases {
		if got := classifyNativeFailure(nil, []byte(test.message), false); got != test.want {
			t.Fatalf("classifyNativeFailure(%q) = %q, want %q", test.message, got, test.want)
		}
	}
}

func TestClassifyNativeFailureSanitizesLifecycleFailures(t *testing.T) {
	cases := []struct {
		message string
		want    NativeFailureClass
	}{
		{"SandboxTokenWriteFailed", NativeFailureStorage},
		{"read-only file system", NativeFailurePermission},
		{"ContainerCreateFailed", NativeFailureDriver},
		{"ContainerStartFailed", NativeFailureDriver},
		{"Container exited", NativeFailureDriver},
		{"sandbox provisioning stream ended before reaching terminal phase", NativeFailureDriver},
		{"sandbox entered error phase while provisioning", NativeFailureDriver},
	}
	for _, test := range cases {
		if got := classifyNativeFailure(nil, []byte(test.message), false); got != test.want {
			t.Fatalf("classifyNativeFailure(%q) = %q, want %q", test.message, got, test.want)
		}
	}
}

func TestClassifyNativeFailureSanitizesSemanticArguments(t *testing.T) {
	cases := []struct {
		message string
		want    NativeFailureClass
	}{
		{
			message: "rpc error: code = InvalidArgument desc = provider binding is invalid",
			want:    NativeFailureArgumentProvider,
		},
		{
			message: "rpc error: code = InvalidArgument desc = label is invalid",
			want:    NativeFailureArgumentLabel,
		},
		{
			message: "rpc error: code = InvalidArgument desc = policy is invalid",
			want:    NativeFailureArgumentPolicy,
		},
		{
			message: "rpc error: code = InvalidArgument desc = image template is invalid",
			want:    NativeFailureArgumentImage,
		},
		{
			message: "rpc error: code = InvalidArgument desc = sandbox name is invalid",
			want:    NativeFailureArgumentName,
		},
		{
			message: "rpc error: code = InvalidArgument desc = command is invalid",
			want:    NativeFailureArgumentCommand,
		},
	}
	for _, test := range cases {
		if got := classifyNativeFailure(nil, []byte(test.message), false); got != test.want {
			t.Errorf("classify %q = %q, want %q", test.message, got, test.want)
		}
	}
}

func TestNativeCommandFailureIsDistinguishableFromProviderBoundaryFailure(t *testing.T) {
	native := nativeCommandFailure(CommandResult{})
	if !IsNativeCommandFailure(native) {
		t.Fatal("native command failure was not identified")
	}
	if IsNativeCommandFailure(ErrProviderFailure) {
		t.Fatal("provider boundary failure was identified as a native command failure")
	}
}
