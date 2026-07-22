//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const routeSupervisorOwnershipSuffix = ".supervisor.lock"
const routeManagerOwnershipSuffix = ".manager.lock"

type linuxRouteOwnership struct {
	file *os.File
}

func acquireRouteSupervisorOwnership(
	config routeSupervisorConfig,
) (routeOwnership, error) {
	return acquireRouteOwnership(config, routeSupervisorOwnershipPath(config.StateFile))
}

func acquireRouteManagerOwnership(
	config routeSupervisorConfig,
) (routeOwnership, error) {
	return acquireRouteOwnership(config, routeManagerOwnershipPath(config.StateFile))
}

func acquireRouteOwnership(
	config routeSupervisorConfig,
	path string,
) (routeOwnership, error) {
	if !filepath.IsAbs(config.StateFile) ||
		filepath.Clean(config.ControlSocket) == path {
		return nil, errors.New("PostgreSQL route ownership path is invalid")
	}
	if err := validateRouteOwnershipDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}

	descriptor, err := syscall.Open(
		path,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, errors.New("open PostgreSQL route ownership")
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = syscall.Close(descriptor)
		return nil, errors.New("open PostgreSQL route ownership")
	}
	if err := validateRouteOwnershipFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := syscall.Flock(descriptor, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("acquire PostgreSQL route ownership")
	}
	return &linuxRouteOwnership{file: file}, nil
}

func routeSupervisorOwnershipPath(stateFile string) string {
	return filepath.Clean(stateFile) + routeSupervisorOwnershipSuffix
}

func routeManagerOwnershipPath(stateFile string) string {
	return filepath.Clean(stateFile) + routeManagerOwnershipSuffix
}

func validateRouteOwnershipDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("invalid PostgreSQL route ownership directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("invalid PostgreSQL route ownership directory")
	}
	return nil
}

func validateRouteOwnershipFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("invalid PostgreSQL route ownership file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("invalid PostgreSQL route ownership file")
	}
	return nil
}

func (ownership *linuxRouteOwnership) Close() error {
	if ownership == nil || ownership.file == nil {
		return errors.New("release PostgreSQL route ownership")
	}
	if err := ownership.file.Close(); err != nil {
		return errors.New("release PostgreSQL route ownership")
	}
	ownership.file = nil
	return nil
}
