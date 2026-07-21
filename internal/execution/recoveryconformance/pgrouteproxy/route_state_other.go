//go:build !unix

package pgrouteproxy

import (
	"errors"
	"os"
)

func lockRouteState(_ *os.File) error {
	return errors.New("PostgreSQL route state requires Unix file locking")
}

func unlockRouteState(_ *os.File) error {
	return nil
}

func routePathOwnedByCurrentUser(_ os.FileInfo) bool {
	return false
}

func routePathSingleLink(_ os.FileInfo) bool {
	return false
}
