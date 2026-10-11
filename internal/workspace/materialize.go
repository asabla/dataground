package workspace

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
)

// Materialize installs one verified snapshot as a new workspace directory.
// The parent must be an owned directory without group or other write access.
// It must be private to the provisioning boundary until this call completes.
// Existing destinations are never changed, including empty directories or links.
func (snapshot *Snapshot) Materialize(ctx context.Context, parent string) (Receipt, error) {
	if snapshot == nil || snapshot.entries == nil || ctx == nil {
		return Receipt{}, ErrSourceInvalid
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if !materializationSupported() {
		return Receipt{}, ErrSourceUnsupported
	}
	root, directory, err := openParent(parent)
	if err != nil {
		return Receipt{}, err
	}
	defer root.Close()
	defer directory.Close()
	if _, err := root.Lstat("workspace"); err == nil {
		return Receipt{}, ErrSourceExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return Receipt{}, ErrSourceUnavailable
	}
	staging := ".dataground-source-" + rand.Text()
	if err := root.Mkdir(staging, 0o700); err != nil {
		return Receipt{}, ErrSourceUnavailable
	}
	defer root.RemoveAll(staging)
	tree, err := root.OpenRoot(staging)
	if err != nil {
		return Receipt{}, ErrSourceUnavailable
	}
	defer tree.Close()
	if err := tree.Chmod(".", 0o700); err != nil {
		return Receipt{}, ErrSourceUnavailable
	}
	for _, name := range snapshot.names {
		if err := ctx.Err(); err != nil {
			return Receipt{}, err
		}
		entry := snapshot.entries[name]
		if entry.kind == tar.TypeDir {
			if err := tree.Mkdir(name, 0o700); err != nil {
				return Receipt{}, ErrSourceUnavailable
			}
			if err := tree.Chmod(name, 0o700); err != nil {
				return Receipt{}, ErrSourceUnavailable
			}
		}
	}
	for _, name := range snapshot.names {
		if err := ctx.Err(); err != nil {
			return Receipt{}, err
		}
		entry := snapshot.entries[name]
		if entry.kind != tar.TypeReg {
			continue
		}
		file, err := tree.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return Receipt{}, ErrSourceUnavailable
		}
		_, writeErr := file.Write(entry.content)
		mode := os.FileMode(0o600)
		if entry.executable {
			mode = 0o700
		}
		modeErr := file.Chmod(mode)
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil || modeErr != nil || syncErr != nil || closeErr != nil {
			return Receipt{}, ErrSourceUnavailable
		}
	}
	// Links are installed after every ordinary file and directory. No archive
	// entry can turn a later extraction path into a link traversal.
	for _, name := range snapshot.names {
		if err := ctx.Err(); err != nil {
			return Receipt{}, err
		}
		entry := snapshot.entries[name]
		if entry.kind == tar.TypeSymlink {
			if err := tree.Symlink(entry.target, name); err != nil {
				return Receipt{}, ErrSourceUnavailable
			}
		}
	}
	for index := len(snapshot.names) - 1; index >= -1; index-- {
		name := "."
		if index >= 0 {
			name = snapshot.names[index]
			if snapshot.entries[name].kind != tar.TypeDir {
				continue
			}
		}
		file, err := tree.Open(name)
		if err != nil {
			return Receipt{}, ErrSourceUnavailable
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if syncErr != nil || closeErr != nil {
			return Receipt{}, ErrSourceUnavailable
		}
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if err := renameExclusive(directory, staging, "workspace"); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Receipt{}, ErrSourceExists
		}
		return Receipt{}, ErrSourceUnavailable
	}
	if err := directory.Sync(); err != nil {
		return Receipt{}, ErrSourceUnavailable
	}
	return snapshot.receipt, nil
}

func openParent(parent string) (*os.Root, *os.File, error) {
	if !materializationSupported() {
		return nil, nil, ErrSourceUnsupported
	}
	if !filepath.IsAbs(parent) || filepath.Clean(parent) != parent {
		return nil, nil, ErrSourceInvalid
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 || !ownedDirectory(info) {
		return nil, nil, ErrSourceUnavailable
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, nil, ErrSourceUnavailable
	}
	directory, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, nil, ErrSourceUnavailable
	}
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Mode().Perm()&0022 != 0 || !ownedDirectory(opened) {
		directory.Close()
		root.Close()
		return nil, nil, ErrSourceUnavailable
	}
	return root, directory, nil
}
