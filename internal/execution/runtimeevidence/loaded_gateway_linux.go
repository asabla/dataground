//go:build linux

package runtimeevidence

import (
	"context"
	"io"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// Observe the mounted inode used by the process, not only the current source
// path. A rewrite followed by a restored mtime still changes Linux ctime.
func checkLoadedGatewayConfiguration(ctx context.Context, pid int, workspace *runtimeTopologyWorkspace, digest string, started time.Time) error {
	if pid <= 0 || workspace == nil || ctx.Err() != nil {
		return ErrDockerTopologyDrift
	}
	root, err := os.OpenRoot("/proc/" + strconv.Itoa(pid) + "/root")
	if err != nil {
		return ErrDockerTopologyDrift
	}
	defer root.Close()
	file, err := root.Open("etc/openshell/gateway.toml")
	if err != nil {
		return ErrDockerTopologyDrift
	}
	defer file.Close()
	var filesystem unix.Statfs_t
	if unix.Fstatfs(int(file.Fd()), &filesystem) != nil {
		return ErrDockerTopologyDrift
	}
	switch filesystem.Type {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC, unix.TMPFS_MAGIC:
	default:
		return ErrDockerTopologyDrift
	}
	before, err := file.Stat()
	if err != nil || !safeRuntimeTopologyFile(before) || !os.SameFile(before, workspace.gatewayInfo) || !gatewayFilePredatesStart(before, started) {
		return ErrDockerTopologyDrift
	}
	content, err := io.ReadAll(io.LimitReader(file, runtimeTopologyMaxFileBytes+1))
	defer clear(content)
	after, statErr := file.Stat()
	if err != nil || statErr != nil || len(content) > runtimeTopologyMaxFileBytes || !runtimeTopologyDigestEqual(runtimeTopologySHA256(content), digest) || !safeRuntimeTopologyFile(after) || !os.SameFile(before, after) || !gatewayFilePredatesStart(after, started) || ctx.Err() != nil {
		return ErrDockerTopologyDrift
	}
	return nil
}

const gatewayTimestampUncertainty = time.Second

func gatewayFilePredatesStart(info os.FileInfo, started time.Time) bool {
	stat, ok := runtimeTopologySystemStat(info)
	return ok && gatewayClockWithinBound() && !info.ModTime().After(started) && time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec).Add(gatewayTimestampUncertainty).Before(started)
}

// ClockGetres reports precision, not a maximum age for the cached coarse clock.
// Require a conservative staging gap and reject a currently stale coarse clock.
func gatewayClockWithinBound() bool {
	var coarse, current unix.Timespec
	if unix.ClockGettime(unix.CLOCK_REALTIME_COARSE, &coarse) != nil || unix.ClockGettime(unix.CLOCK_REALTIME, &current) != nil {
		return false
	}
	age := time.Duration(current.Nano() - coarse.Nano())
	return age >= 0 && age < gatewayTimestampUncertainty
}
