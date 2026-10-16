//go:build linux || darwin

package workspace

import (
	"os"
	"syscall"
)

func openCaptureNode(root *os.Root, name string, directory bool) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	return root.OpenFile(name, flags, 0)
}
func sameCaptureDevice(left, right os.FileInfo) bool {
	a, aOK := left.Sys().(*syscall.Stat_t)
	b, bOK := right.Sys().(*syscall.Stat_t)
	return aOK && bOK && a.Dev == b.Dev
}
