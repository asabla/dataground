//go:build unix

package runtimeevidence

import (
	"os"
	"path/filepath"
	"syscall"
)

func lockDevelopmentGateway(root, runID string) (*os.File, error) {
	info, err := os.Lstat(root)
	if err != nil || !safeRuntimeTopologyDirectory(info) {
		return nil, ErrDevelopmentGateway
	}
	path := filepath.Join(root, "dg-runtime-deployment-"+runID+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, ErrDevelopmentGateway
	}
	fail := func() (*os.File, error) { _ = file.Close(); return nil, ErrDevelopmentGateway }
	opened, err := file.Stat()
	current, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !safeRuntimeTopologyFile(opened) || !safeRuntimeTopologyFile(current) || !os.SameFile(opened, current) || syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return fail()
	}
	current, err = os.Lstat(path)
	if err != nil || !os.SameFile(opened, current) {
		return fail()
	}
	return file, nil
}

func closeDevelopmentGatewayLock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}
