package runtimeevidence

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func readDevelopmentBytes(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !safeRuntimeTopologyFile(before) || before.Size() > 16<<10 {
		return nil, ErrDevelopmentGateway
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrDevelopmentGateway
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, ErrDevelopmentGateway
	}
	content, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	after, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !safeRuntimeTopologyFile(after) || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || len(content) > 16<<10 {
		clear(content)
		return nil, ErrDevelopmentGateway
	}
	return content, nil
}

func readDevelopmentRecord(path string, expected []byte) (bool, error) {
	content, err := readDevelopmentBytes(path)
	defer clear(content)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !bytes.Equal(content, expected) {
		return false, ErrDevelopmentGateway
	}
	return true, nil
}

// Records are immutable and published under the stable per-run lock. A crash
// before rename leaves only an unused temporary file; it cannot publish half a
// request or authorize a repeated native start.
func immutableDevelopmentRecord(path string, content []byte, create bool) error {
	found, err := readDevelopmentRecord(path, content)
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	if !create {
		return ErrDevelopmentGateway
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return ErrDevelopmentGateway
	}
	defer parent.Close()
	info, err := parent.Stat()
	if err != nil || !safeRuntimeTopologyDirectory(info) {
		return ErrDevelopmentGateway
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".dg-deployment-record-")
	if err != nil {
		return ErrDevelopmentGateway
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if file.Chmod(0o600) != nil {
		return ErrDevelopmentGateway
	}
	if _, err = file.Write(content); err != nil {
		return ErrDevelopmentGateway
	}
	if file.Sync() != nil || file.Close() != nil {
		return ErrDevelopmentGateway
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return ErrDevelopmentGateway
	}
	if os.Rename(file.Name(), path) != nil || parent.Sync() != nil {
		return ErrDevelopmentGateway
	}
	return nil
}
