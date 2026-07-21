package main

import (
	"os"
	"testing"
)

func TestValidateRouteChildOwnershipRejectsMismatchedParent(t *testing.T) {
	if err := validateRouteChildOwnership(os.Getppid() + 1); err == nil {
		t.Fatal("route child accepted a mismatched supervisor process")
	}
}

func TestValidateRouteChildOwnershipAcceptsExpectedParent(t *testing.T) {
	if err := validateRouteChildOwnership(os.Getppid()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRouteChildOwnershipAllowsUnsupervisedServeMode(t *testing.T) {
	if err := validateRouteChildOwnership(0); err != nil {
		t.Fatal(err)
	}
}
