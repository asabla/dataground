//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const routeSupervisorOwnershipSuffix = ".supervisor.lock"

type linuxRouteSupervisorOwnership struct {
	file *os.File
}

func acquireRouteSupervisorOwnership(
	config routeSupervisorConfig,
) (routeSupervisorOwnership, error) {
	path := routeSupervisorOwnershipPath(config.StateFile)
	if !filepath.IsAbs(config.StateFile) ||
		filepath.Clean(config.ControlSocket) == path {
		return nil, errors.New("PostgreSQL route supervisor ownership path is invalid")
	}
	if err := validateRouteSupervisorOwnershipDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}

	descriptor, err := syscall.Open(
		path,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, errors.New("open PostgreSQL route supervisor ownership")
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = syscall.Close(descriptor)
		return nil, errors.New("open PostgreSQL route supervisor ownership")
	}
	if err := validateRouteSupervisorOwnershipFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := syscall.Flock(descriptor, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("acquire PostgreSQL route supervisor ownership")
	}
	return &linuxRouteSupervisorOwnership{file: file}, nil
}

func routeSupervisorOwnershipPath(stateFile string) string {
	return filepath.Clean(stateFile) + routeSupervisorOwnershipSuffix
}

func validateRouteSupervisorOwnershipDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("invalid PostgreSQL route supervisor ownership directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("invalid PostgreSQL route supervisor ownership directory")
	}
	return nil
}

func validateRouteSupervisorOwnershipFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("invalid PostgreSQL route supervisor ownership file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("invalid PostgreSQL route supervisor ownership file")
	}
	return nil
}

func (ownership *linuxRouteSupervisorOwnership) Close() error {
	if ownership == nil || ownership.file == nil {
		return errors.New("release PostgreSQL route supervisor ownership")
	}
	if err := ownership.file.Close(); err != nil {
		return errors.New("release PostgreSQL route supervisor ownership")
	}
	ownership.file = nil
	return nil
}
