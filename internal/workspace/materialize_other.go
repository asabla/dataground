//go:build !linux && !darwin

package workspace

import "os"

func materializationSupported() bool                 { return false }
func ownedDirectory(os.FileInfo) bool                { return false }
func renameExclusive(*os.File, string, string) error { return ErrSourceUnsupported }

func singleLink(os.FileInfo) bool { return false }
