//go:build !unix

package canaryworkspace

import "os"

func ownedByCurrentUser(_ os.FileInfo) bool { return false }
