//go:build !unix

package runtimeevidence

import "os"

func runtimeCredentialOwnedByCurrentUser(_ os.FileInfo) bool { return false }

func runtimeCredentialSingleLink(_ os.FileInfo) bool { return false }
