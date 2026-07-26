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
