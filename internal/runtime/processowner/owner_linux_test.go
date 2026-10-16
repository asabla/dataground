//go:build linux && (amd64 || arm64)

package processowner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNativeProcessOwnerKernelBoundary(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("dedicated process owner requires an unprivileged test runner")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for adversarial process fixtures")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// This sibling must survive cleanup of the owner's descendants.
	sibling := exec.CommandContext(ctx, python, "-c", "import time; time.sleep(25)")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sibling.Process.Kill(); _ = sibling.Wait() }()
	for _, mode := range []string{"descendants", "orphaned", "forking", "concurrent-stop", "cancelled-stop", "existing-child", "empty-stop", "failed-start"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeProcessOwnerChild$")
			command.Env = append(os.Environ(), "DATAGROUND_PROCESS_OWNER_CHILD="+mode, "DATAGROUND_PROCESS_OWNER_PYTHON="+python)
			out, err := command.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "OWNER_TEST_OK") {
				t.Fatalf("owner fixture %s failed: %s (%v)", mode, out, err)
			}
			if err := sibling.Process.Signal(unix.Signal(0)); err != nil {
				t.Fatal("cleanup touched unrelated sibling")
			}
		})
	}
}

func TestNativeProcessOwnerChild(t *testing.T) {
	mode := os.Getenv("DATAGROUND_PROCESS_OWNER_CHILD")
	if mode == "" {
		t.Skip("isolated owner subprocess only")
	}
	python := os.Getenv("DATAGROUND_PROCESS_OWNER_PYTHON")
	if mode == "existing-child" {
		child := exec.Command(python, "-c", "import time; time.sleep(20)")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
		if _, err := New(); !errors.Is(err, ErrState) {
			t.Fatal("accepted a shared process", err)
		}
		if err := child.Process.Signal(unix.Signal(0)); err != nil {
			t.Fatal("constructor changed an unrelated child")
		}
		fmt.Println("OWNER_TEST_OK")
		return
	}
	owner, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(); !errors.Is(err, ErrState) {
		t.Fatal("duplicate owner admitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	defer owner.Stop(context.Background())
	// Even copied private state cannot create another owner for this host.
	foreign := &Owner{pid: owner.pid, sigchld: owner.sigchld}
	if _, err := foreign.Start(python, []string{python}, nil); !errors.Is(err, ErrState) {
		t.Fatal("copied owner admitted a native start")
	}
	if err := foreign.Stop(ctx); !errors.Is(err, ErrState) {
		t.Fatal("copied owner admitted terminal cleanup")
	}
	if mode == "empty-stop" || mode == "failed-start" {
		if mode == "failed-start" {
			if _, err := owner.Start("/dataground-missing-native-entry", []string{"/dataground-missing-native-entry"}, nil); !errors.Is(err, ErrUnavailable) {
				t.Fatal("missing entry point did not fail closed", err)
			}
		}
		if err := owner.Stop(ctx); err != nil {
			t.Fatal("empty owner could not stop", err)
		}
		if _, err := owner.Start(python, []string{python}, nil); !errors.Is(err, ErrState) {
			t.Fatal("terminal owner admitted a start")
		}
		fmt.Println("OWNER_TEST_OK")
		return
	}
	stream, err := owner.Start(python, []string{python, "-c", processFixture, strconv.Itoa(unix.SYS_CLONE), strconv.Itoa(unix.SYS_CLONE3), strconv.Itoa(unix.SYS_PTRACE), strconv.Itoa(unix.SYS_PROCESS_VM_WRITEV), mode}, []string{"PATH=/usr/bin:/bin"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Output.Close()
	defer stream.Errors.Close()
	defer stream.Input.Close()
	diagnostic := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(io.LimitReader(stream.Errors, 4096)); diagnostic <- data }()
	scanner := bufio.NewScanner(stream.Output)
	if !scanner.Scan() {
		t.Fatal("fixture did not reach readiness", string(<-diagnostic))
	}
	var proof struct {
		ParentDenied, Clone3Denied, PtraceDenied, WritevDenied, MemoryDenied, PipeDenied bool
		PIDs                                                                             []int
	}
	if json.Unmarshal(scanner.Bytes(), &proof) != nil || !proof.ParentDenied || !proof.Clone3Denied || !proof.PtraceDenied || !proof.WritevDenied || !proof.MemoryDenied || !proof.PipeDenied || len(proof.PIDs) != 3 {
		t.Fatalf("ancestry or memory guard missing: %s", scanner.Bytes())
	}
	// These detached descendants are live before terminal cleanup.
	for _, pid := range proof.PIDs {
		if unix.Kill(pid, 0) != nil {
			t.Fatal("detached fixture was not live")
		}
	}
	if mode == "orphaned" {
		exit, err := owner.Wait(ctx)
		if err != nil || !exit.Success {
			t.Fatal("native exit was not observed", err)
		}
		if replay, err := owner.Wait(ctx); err != nil || replay != exit {
			t.Fatal("native exit replay changed", err)
		}
		for _, pid := range proof.PIDs {
			if unix.Kill(pid, 0) != nil {
				t.Fatal("native exit observation stopped a descendant")
			}
		}
		adopted := false
		for !adopted && ctx.Err() == nil {
			children, err := directChildren(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			for _, pid := range children {
				for _, expected := range proof.PIDs {
					if pid == expected {
						adopted = true
					}
				}
			}
			if !adopted {
				time.Sleep(time.Millisecond)
			}
		}
		if !adopted {
			t.Fatal("orphan was not adopted by owner")
		}
	}
	if _, err := owner.Start(python, []string{python}, nil); !errors.Is(err, ErrState) {
		t.Fatal("second start admitted")
	}
	if mode == "cancelled-stop" {
		cancelled, stop := context.WithCancel(ctx)
		stop()
		if _, err := owner.Wait(cancelled); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled native wait succeeded", err)
		}
		if err := owner.Stop(cancelled); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled stop succeeded", err)
		}
		if _, err := owner.Start(python, []string{python}, nil); !errors.Is(err, ErrState) {
			t.Fatal("failed stop reopened starts")
		}
	}
	if mode == "concurrent-stop" {
		waits := make(chan error, 4)
		for range 4 {
			go func() {
				result, err := owner.Wait(ctx)
				if err == nil && result.Success {
					err = ErrState
				}
				waits <- err
			}()
		}
		var wg sync.WaitGroup
		results := make(chan error, 4)
		for range 4 {
			wg.Add(1)
			go func() { defer wg.Done(); results <- owner.Stop(ctx) }()
		}
		wg.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
		for range 4 {
			if err := <-waits; err != nil {
				t.Fatal("native wait competed with terminal cleanup", err)
			}
		}
	} else if err := owner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	exit, err := owner.Wait(ctx)
	if err != nil || exit.Success != (mode == "orphaned") {
		t.Fatal("terminal native result differs", exit, err)
	}
	for _, pid := range proof.PIDs {
		if !errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			t.Fatal("descendant survived terminal stop")
		}
	}
	if err := owner.Stop(ctx); err != nil {
		t.Fatal("completed stop replay failed", err)
	}
	if _, err := owner.Start(python, []string{python}, nil); !errors.Is(err, ErrState) {
		t.Fatal("terminal stop reopened starts")
	}
	if data := <-diagnostic; len(data) != 0 {
		t.Fatal("fixture diagnostics", string(data))
	}
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_ALL, 0, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT|unix.WALL, nil); !errors.Is(err, unix.ECHILD) {
		t.Fatal("kernel still reports children")
	}
	fmt.Println("OWNER_TEST_OK")
}

const processFixture = `import ctypes,errno,json,os,signal,sys,threading,time
libc=ctypes.CDLL(None,use_errno=True)
proof={}
thread=threading.Thread(target=lambda: None);thread.start();thread.join()
# Python join can return before the kernel removes the finished thread.
# Do not fork the Python interpreter while that thread is still present.
end=time.monotonic()+1
while len(os.listdir('/proc/self/task'))!=1 and time.monotonic()<end: time.sleep(0.001)
if len(os.listdir('/proc/self/task'))!=1: raise RuntimeError('native thread did not exit')
def denied(n,expected,*args):
 ctypes.set_errno(0)
 result=libc.syscall(n,*args)
 if result==0 and n==int(sys.argv[1]): os._exit(90)
 return result==-1 and ctypes.get_errno()==expected
proof['ParentDenied']=denied(int(sys.argv[1]),errno.EPERM,0x8000|signal.SIGCHLD,0,0,0,0)
proof['Clone3Denied']=denied(int(sys.argv[2]),errno.ENOSYS,0,0)
proof['PtraceDenied']=denied(int(sys.argv[3]),errno.EPERM,0,0,0,0)
proof['WritevDenied']=denied(int(sys.argv[4]),errno.EPERM,os.getppid(),0,0,0,0,0)
try:
 fd=os.open('/proc/'+str(os.getppid())+'/mem',os.O_RDONLY);os.close(fd);proof['MemoryDenied']=False
except PermissionError: proof['MemoryDenied']=True
try:
 fd=os.open('/proc/'+str(os.getppid())+'/fd/1',os.O_WRONLY);os.close(fd);proof['PipeDenied']=False
except PermissionError: proof['PipeDenied']=True
other=libc.syscall(int(sys.argv[1]),0,0,0,0,0)
if other==0: time.sleep(12);os._exit(0)
if other<0: raise RuntimeError('ordinary clone failed')
r,w=os.pipe()
pid=os.fork()
if pid==0:
 os.close(r);os.setsid()
 other=os.fork()
 if other==0:
  os.write(w,(str(os.getpid())+'\n').encode());os.close(w)
  signal.signal(signal.SIGTERM,signal.SIG_IGN);time.sleep(12);os._exit(0)
 os.write(w,(str(os.getpid())+'\n').encode());os.close(w)
 signal.signal(signal.SIGTERM,signal.SIG_IGN);time.sleep(12);os._exit(0)
os.close(w)
with os.fdopen(r) as f: proof['PIDs']=[other]+[int(x) for x in f.read().splitlines()]
print(json.dumps(proof),flush=True)
if sys.argv[5]=='orphaned': os._exit(0)
if sys.argv[5]=='forking':
 for _ in range(100):
  p=os.fork()
  if p==0: time.sleep(1);os._exit(0)
  time.sleep(0.001)
time.sleep(12)
`
