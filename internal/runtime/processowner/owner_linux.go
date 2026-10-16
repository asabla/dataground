//go:build linux && (amd64 || arm64)

package processowner

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

var claimed atomic.Bool
var activeOwner atomic.Pointer[Owner]

// Owner is process-wide and irreversible. Its host must be a dedicated,
// unprivileged process with no existing children, and must create children only
// through Start. Never construct it in an API server, worker or shared runner.
// A failed construction can leave protections installed; the host must exit.
type Owner struct {
	mu                       sync.Mutex
	pid                      int
	started, sealed, stopped bool
	input                    *os.File
	sigchld                  chan os.Signal
}

func New() (*Owner, error) {
	if !claimed.CompareAndSwap(false, true) {
		return nil, ErrState
	}
	uid, euid, suid := unix.Getresuid()
	gid, egid, sgid := unix.Getresgid()
	if uid <= 0 || uid != euid || uid != suid || gid <= 0 || gid != egid || gid != sgid {
		return nil, ErrUnavailable
	}
	status, err := boundedRead("/proc/self/status", 64<<10)
	if err != nil {
		return nil, ErrUnavailable
	}
	for _, field := range []string{"CapInh:", "CapPrm:", "CapEff:", "CapAmb:"} {
		found := false
		for _, line := range strings.Split(string(status), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 && parts[0] == field {
				value, err := strconv.ParseUint(parts[1], 16, 64)
				if err != nil || value != 0 {
					return nil, ErrUnavailable
				}
				found = true
			}
		}
		if !found {
			return nil, ErrUnavailable
		}
	}
	var fs unix.Statfs_t
	if unix.Statfs("/proc", &fs) != nil || uint64(fs.Type) != uint64(unix.PROC_SUPER_MAGIC) {
		return nil, ErrUnavailable
	}
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_ALL, 0, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT|unix.WALL, nil); !errors.Is(err, unix.ECHILD) {
		return nil, ErrState
	}
	if unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0) != nil || unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != nil {
		return nil, ErrUnavailable
	}
	// Go's installed handler does not reap children or set SA_NOCLDWAIT.
	// Keep it installed for the host's lifetime, including after terminal stop.
	// The host must never install another handler or introduce another waiter.
	sigchld := make(chan os.Signal, 1)
	signal.Notify(sigchld, unix.SIGCHLD)
	owner := &Owner{pid: os.Getpid(), sigchld: sigchld}
	activeOwner.Store(owner)
	return owner, nil
}

// Start creates one trusted, fixed native entry point. The host chooses all
// arguments and environment; none of these parameters may come from a public
// arbitrary-command API. The inherited filter is installed before this fork.
func (owner *Owner) Start(path string, args, env []string) (Streams, error) {
	if owner == nil || activeOwner.Load() != owner {
		return Streams{}, ErrState
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.pid != os.Getpid() || owner.started || owner.sealed || !filepath.IsAbs(path) || len(args) == 0 || args[0] != path {
		return Streams{}, ErrState
	}
	owner.started = true
	// PR_SET_SECCOMP is current-thread only. Pin installation and fork to the
	// same OS thread; the native process inherits the filter before exec.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if installAncestryFilter() != nil {
		return Streams{}, ErrUnavailable
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return Streams{}, ErrUnavailable
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return Streams{}, ErrUnavailable
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return Streams{}, ErrUnavailable
	}
	process, err := os.StartProcess(path, args, &os.ProcAttr{Env: env, Files: []*os.File{inR, outW, errW}})
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return Streams{}, ErrUnavailable
	}
	// Stop owns wait4 for every descendant, including non-SIGCHLD clone children.
	// Releasing Go's handle prevents a second waiter from competing with it.
	_ = process.Release()
	owner.input = inW
	return Streams{Input: inW, Output: outR, Errors: errR}, nil
}

// Stop permanently seals starts, kills owned descendants, and
// returns success only after the kernel reports ECHILD with __WALL. A cancelled
// or failed stop remains sealed and may be retried; it never permits export.
// Only a successful result can supply the host's terminal quiescence boundary.
func (owner *Owner) Stop(ctx context.Context) error {
	if owner == nil || activeOwner.Load() != owner || ctx == nil {
		return ErrState
	}
	for !owner.mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer owner.mu.Unlock()
	if owner.pid != os.Getpid() {
		return ErrState
	}
	owner.sealed = true
	if owner.input != nil {
		_ = owner.input.Close()
	}
	if owner.stopped {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		children, err := directChildren(owner.pid)
		if err != nil {
			return ErrUnavailable
		}
		// No child is reaped until this complete signal pass ends. Exited
		// direct children remain zombies, so their PIDs cannot be reused.
		for _, pid := range children {
			if err := killOwnedChild(owner.pid, pid); err != nil {
				return ErrUnavailable
			}
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			pid, err := unix.Wait4(-1, nil, unix.WNOHANG|unix.WALL, nil)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.ECHILD) {
				owner.stopped = true
				return nil
			}
			if err != nil {
				return ErrUnavailable
			}
			if pid == 0 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func directChildren(parent int) ([]int, error) {
	tasks, err := os.ReadDir("/proc/" + strconv.Itoa(parent) + "/task")
	if err != nil || len(tasks) > 4096 {
		return nil, ErrUnavailable
	}
	seen := make(map[int]bool)
	var result []int
	for _, task := range tasks {
		if _, err := strconv.ParseInt(task.Name(), 10, 32); err != nil {
			return nil, ErrUnavailable
		}
		data, err := boundedRead("/proc/"+strconv.Itoa(parent)+"/task/"+task.Name()+"/children", 1<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.ParseInt(field, 10, 32)
			if err != nil || pid <= 0 || int(pid) == parent {
				return nil, ErrUnavailable
			}
			if !seen[int(pid)] {
				seen[int(pid)] = true
				result = append(result, int(pid))
			}
			if len(result) > 65536 {
				return nil, ErrUnavailable
			}
		}
	}
	return result, nil
}

func killOwnedChild(parent, pid int) error {
	// The dedicated owner is the sole waiter. A direct child cannot disappear
	// or have its PID reused during this pass, even if it exits concurrently.
	data, err := boundedRead("/proc/"+strconv.Itoa(pid)+"/stat", 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return ErrUnavailable
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 2 {
		return ErrUnavailable
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return ErrUnavailable
	}
	if ppid != parent {
		return nil
	}
	err = unix.Kill(pid, unix.SIGKILL)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func boundedRead(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, ErrUnavailable
	}
	return data, nil
}
