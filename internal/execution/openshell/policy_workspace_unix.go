//go:build unix

package openshell

import (
	"errors"
	"os"
	"syscall"
)

var errPolicyWorkspaceLocked = errors.New("policy workspace lock is held")

func lockPolicyWorkspace(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errPolicyWorkspaceLocked
	}
	return err
}

func unlockPolicyWorkspace(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func policyWorkspaceOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func policyWorkspaceSingleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
