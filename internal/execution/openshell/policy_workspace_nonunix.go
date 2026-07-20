//go:build !unix

package openshell

import (
	"errors"
	"os"
)

var errPolicyWorkspaceLocked = errors.New("policy workspace lock is held")

func lockPolicyWorkspace(_ *os.File) error {
	return errors.New("policy workspace locking is not supported on this platform")
}

func unlockPolicyWorkspace(_ *os.File) error { return nil }

func policyWorkspaceOwnedByCurrentUser(_ os.FileInfo) bool { return false }

func policyWorkspaceSingleLink(_ os.FileInfo) bool { return false }
