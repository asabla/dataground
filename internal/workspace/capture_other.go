//go:build !linux && !darwin

package workspace

import "os"

func openCaptureNode(*os.Root, string, bool) (*os.File, error) { return nil, ErrSourceUnsupported }
func sameCaptureDevice(os.FileInfo, os.FileInfo) bool          { return false }
