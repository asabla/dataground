package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path"
)

// Verify observes a complete normalized tree without changing it. It proves
// correspondence to the supplied snapshot, not historical provenance or a lease.
// The caller must keep the workspace inactive throughout verification.
func (snapshot *Snapshot) Verify(ctx context.Context, parent string) (Receipt, error) {
	if snapshot == nil || snapshot.entries == nil || ctx == nil {
		return Receipt{}, ErrSourceInvalid
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	root, directory, err := openParent(parent)
	if err != nil {
		return Receipt{}, err
	}
	defer root.Close()
	defer directory.Close()
	info, err := root.Lstat("workspace")
	if err != nil || info.Mode() != os.ModeDir|0o700 || !ownedDirectory(info) {
		return Receipt{}, ErrSourceInvalid
	}
	tree, err := root.OpenRoot("workspace")
	if err != nil {
		return Receipt{}, ErrSourceUnavailable
	}
	defer tree.Close()
	opened, err := tree.Lstat(".")
	if err != nil || !os.SameFile(info, opened) {
		return Receipt{}, ErrSourceInvalid
	}
	seen := 0
	directories := []string{"."}
	for len(directories) != 0 {
		name := directories[0]
		directories = directories[1:]
		file, err := tree.Open(name)
		if err != nil {
			return Receipt{}, ErrSourceUnavailable
		}
		for {
			if err := ctx.Err(); err != nil {
				file.Close()
				return Receipt{}, err
			}
			entries, readErr := file.ReadDir(128)
			for _, entry := range entries {
				child := path.Join(name, entry.Name())
				if err := snapshot.verifyEntry(tree, child); err != nil {
					file.Close()
					return Receipt{}, err
				}
				seen++
				if seen > len(snapshot.entries) {
					file.Close()
					return Receipt{}, ErrSourceInvalid
				}
				if snapshot.entries[child].kind == tar.TypeDir {
					directories = append(directories, child)
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				file.Close()
				return Receipt{}, ErrSourceUnavailable
			}
		}
		if err := file.Close(); err != nil {
			return Receipt{}, ErrSourceUnavailable
		}
	}

	if seen != len(snapshot.entries) {
		return Receipt{}, ErrSourceInvalid
	}
	return snapshot.receipt, nil
}

func (snapshot *Snapshot) verifyEntry(tree *os.Root, name string) error {
	expected, found := snapshot.entries[name]
	if !found {
		return ErrSourceInvalid
	}
	info, err := tree.Lstat(name)
	if err != nil || !ownedDirectory(info) {
		return ErrSourceInvalid
	}
	switch expected.kind {
	case tar.TypeDir:
		if info.Mode() != os.ModeDir|0o700 {
			return ErrSourceInvalid
		}
	case tar.TypeSymlink:
		if info.Mode()&os.ModeSymlink == 0 {
			return ErrSourceInvalid
		}
		target, err := tree.Readlink(name)
		if err != nil || target != expected.target {
			return ErrSourceInvalid
		}
	case tar.TypeReg:
		mode := os.FileMode(0o600)
		if expected.executable {
			mode = 0o700
		}
		if info.Mode() != mode || !singleLink(info) || info.Size() != int64(len(expected.content)) {
			return ErrSourceInvalid
		}
		file, err := tree.Open(name)
		if err != nil {
			return ErrSourceUnavailable
		}
		opened, statErr := file.Stat()
		content, readErr := io.ReadAll(io.LimitReader(file, int64(len(expected.content))+1))
		closeErr := file.Close()
		current, currentErr := tree.Lstat(name)
		equal := bytes.Equal(content, expected.content)
		clear(content)
		if statErr != nil || readErr != nil || closeErr != nil || currentErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, current) || !equal {
			return ErrSourceInvalid
		}
	default:
		return ErrSourceInvalid
	}
	return nil
}
