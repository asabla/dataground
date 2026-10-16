package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
)

var ErrCaptureUnavailable = errors.New("workspace capture is unavailable")

// Capture reads the image-owned workspace without modifying it. The trusted
// caller must quiesce the entire workload and retain that fence through export.
// Two matching reads detect observed drift; they do not establish quiescence or
// an atomic filesystem snapshot. No host path or link target enters a receipt.
func Capture(ctx context.Context, parent string) (*Snapshot, error) {
	if ctx == nil {
		return nil, ErrCaptureUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	first, err := captureOnce(ctx, parent)
	if err != nil {
		return nil, err
	}
	second, err := captureOnce(ctx, parent)
	if err != nil {
		first.discard()
		return nil, err
	}
	defer second.discard()
	if err := ctx.Err(); err != nil {
		first.discard()
		return nil, err
	}
	if !bytes.Equal(first.archive, second.archive) {
		first.discard()
		return nil, ErrCaptureUnavailable
	}
	return first, nil
}

func captureOnce(ctx context.Context, parent string) (*Snapshot, error) {
	root, directory, err := openParent(parent)
	if err != nil {
		return nil, ErrCaptureUnavailable
	}
	defer root.Close()
	defer directory.Close()
	info, err := root.Lstat("workspace")
	if err != nil || !safeCaptureInfo(info) || !info.IsDir() {
		return nil, ErrCaptureUnavailable
	}
	tree, err := root.OpenRoot("workspace")
	if err != nil {
		return nil, ErrCaptureUnavailable
	}
	defer tree.Close()
	opened, err := tree.Lstat(".")
	if err != nil || !sameCaptureInfo(info, opened) {
		return nil, ErrCaptureUnavailable
	}
	entries := map[string]sourceEntry{}
	defer func() {
		for _, entry := range entries {
			clear(entry.content)
		}
	}()
	pending := []string{"."}
	var total int64
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		before, err := tree.Lstat(name)
		if err != nil || !safeCaptureInfo(before) || !before.IsDir() || !sameCaptureDevice(info, before) {
			return nil, ErrCaptureUnavailable
		}
		file, err := openCaptureNode(tree, name, true)
		if err != nil {
			return nil, ErrCaptureUnavailable
		}
		stat, err := file.Stat()
		if err != nil || !sameCaptureInfo(before, stat) {
			file.Close()
			return nil, ErrCaptureUnavailable
		}
		for {
			if ctx.Err() != nil {
				file.Close()
				return nil, ctx.Err()
			}
			children, readErr := file.ReadDir(128)
			for _, child := range children {
				name := path.Join(name, child.Name())
				if !validSourcePath(name) || len(entries) >= MaximumEntries {
					file.Close()
					return nil, ErrCaptureUnavailable
				}
				if _, duplicate := entries[name]; duplicate {
					file.Close()
					return nil, ErrCaptureUnavailable
				}
				entry, err := captureEntry(ctx, tree, name, info, MaximumSourceBytes-total)
				if err != nil {
					file.Close()
					return nil, err
				}
				total += int64(len(entry.content))
				entries[name] = entry
				if entry.kind == tar.TypeDir {
					pending = append(pending, name)
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				file.Close()
				return nil, ErrCaptureUnavailable
			}
		}
		after, statErr := file.Stat()
		closeErr := file.Close()
		current, currentErr := tree.Lstat(name)
		if statErr != nil || closeErr != nil || currentErr != nil || !sameCaptureInfo(before, after) || !sameCaptureInfo(before, current) {
			return nil, ErrCaptureUnavailable
		}
	}
	current, err := root.Lstat("workspace")
	if err != nil || !sameCaptureInfo(info, current) {
		return nil, ErrCaptureUnavailable
	}
	result, err := snapshotFromEntries(entries)
	if err != nil {
		return nil, ErrCaptureUnavailable
	}
	return result, nil
}

func captureEntry(ctx context.Context, tree *os.Root, name string, rootInfo os.FileInfo, remaining int64) (sourceEntry, error) {
	entry := sourceEntry{name: name}
	info, err := tree.Lstat(name)
	if err != nil || !safeCaptureInfo(info) || !sameCaptureDevice(rootInfo, info) {
		return sourceEntry{}, ErrCaptureUnavailable
	}
	switch {
	case info.IsDir():
		entry.kind = tar.TypeDir
	case info.Mode()&os.ModeSymlink != 0:
		target, err := tree.Readlink(name)
		current, currentErr := tree.Lstat(name)
		if err != nil || currentErr != nil || !sameCaptureInfo(info, current) || !validLinkText(target) {
			return sourceEntry{}, ErrCaptureUnavailable
		}
		entry.kind = tar.TypeSymlink
		entry.target = target
	case info.Mode().IsRegular():
		if !singleLink(info) || info.Size() < 0 || info.Size() > remaining {
			return sourceEntry{}, ErrCaptureUnavailable
		}
		file, err := openCaptureNode(tree, name, false)
		if err != nil {
			return sourceEntry{}, ErrCaptureUnavailable
		}
		opened, err := file.Stat()
		if err != nil || !sameCaptureInfo(info, opened) || !singleLink(opened) {
			file.Close()
			return sourceEntry{}, ErrCaptureUnavailable
		}
		var content bytes.Buffer
		buffer := make([]byte, 32*1024)
		defer clear(buffer)
		reader := io.LimitReader(file, info.Size()+1)
		for {
			if ctx.Err() != nil {
				file.Close()
				clear(content.Bytes())
				return sourceEntry{}, ctx.Err()
			}
			n, readErr := reader.Read(buffer)
			content.Write(buffer[:n])
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				file.Close()
				clear(content.Bytes())
				return sourceEntry{}, ErrCaptureUnavailable
			}
		}
		after, statErr := file.Stat()
		closeErr := file.Close()
		current, currentErr := tree.Lstat(name)
		if statErr != nil || closeErr != nil || currentErr != nil || !sameCaptureInfo(info, after) || !sameCaptureInfo(info, current) || !singleLink(after) || !singleLink(current) || int64(content.Len()) != info.Size() {
			clear(content.Bytes())
			return sourceEntry{}, ErrCaptureUnavailable
		}
		entry.kind = tar.TypeReg
		entry.content = content.Bytes()
		entry.executable = info.Mode().Perm()&0o111 != 0
	default:
		return sourceEntry{}, ErrCaptureUnavailable
	}
	return entry, nil
}

func safeCaptureInfo(info os.FileInfo) bool {
	if info == nil || !ownedDirectory(info) {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return info.Mode().Type() == os.ModeSymlink
	}
	return (info.IsDir() || info.Mode().IsRegular()) && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && info.Mode().Perm()&0o022 == 0
}
func sameCaptureInfo(left, right os.FileInfo) bool {
	return safeCaptureInfo(left) && safeCaptureInfo(right) && os.SameFile(left, right) && left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}
