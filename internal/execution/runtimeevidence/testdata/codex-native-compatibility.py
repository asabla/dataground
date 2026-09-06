"""Credential-free stock/candidate comparison inside the pinned OpenShell sandbox."""
import json
import os
from pathlib import Path
import selectors
import subprocess
import tempfile
import time

ROOT = Path(tempfile.mkdtemp(prefix="dataground-native-compat-", dir="/tmp"))
WORKSPACE = ROOT / "workspace"
OUTSIDE = ROOT / "outside"
WORKSPACE.mkdir()
OUTSIDE.mkdir()
CANDIDATE = Path("/opt/dataground-compatibility")
stock = CANDIDATE / "stock-codex"
records = []

PROBE = r'''
import ctypes
import json
from pathlib import Path
import socket
import sys

results = {}
for key, path in [("workspaceWrite", sys.argv[1]), ("outsideWrite", sys.argv[2])]:
    try:
        Path(path).write_text("probe")
        results[key] = "allowed"
    except OSError as error:
        results[key] = "denied" if error.errno in [1, 13, 30] else "error"
for key, family, kind in [
    ("inetSocket", socket.AF_INET, socket.SOCK_STREAM),
    ("rawSocket", socket.AF_PACKET, socket.SOCK_RAW),
]:
    try:
        value = socket.socket(family, kind)
        value.close()
        results[key] = "allowed"
    except OSError as error:
        results[key] = "denied" if error.errno in [1, 13] else "error"
lib = ctypes.CDLL(None, use_errno=True)
result = lib.unshare(0x10000000)
results["userNamespace"] = "denied" if result == -1 and ctypes.get_errno() in [1, 13] else "allowed"
print(json.dumps(results))
'''


def record(value):
    records.append(value)
    print(json.dumps(value), flush=True)


def probe_command(name):
    return ["python3", "-c", PROBE, str(WORKSPACE / name), str(OUTSIDE / name)]


def helper_arguments(mode, name):
    policy = {"type": mode}
    if mode == "workspace-write":
        policy.update(writable_roots=[str(WORKSPACE)], network_access=False,
                      exclude_tmpdir_env_var=True, exclude_slash_tmp=True)
    return ["codex-linux-sandbox", "--sandbox-policy", json.dumps(policy),
            "--sandbox-policy-cwd", str(WORKSPACE), "--use-legacy-landlock",
            "--", *probe_command(name)]


for candidate, binary in [("pinned", stock), ("experimental", CANDIDATE / "codex-linux-sandbox")]:
    for mode in ["read-only", "workspace-write"]:
        result = subprocess.run(helper_arguments(mode, candidate + "-" + mode),
                                executable=str(binary), cwd=str(WORKSPACE),
                                text=True, capture_output=True, timeout=15)
        observations = json.loads(result.stdout) if result.returncode == 0 else None
        record({"surface": "native-helper", "candidate": candidate, "mode": mode,
                "exitCode": result.returncode, "installationRejected": "SeccompInstall" in result.stderr,
                "observations": observations})


for candidate, command in [
    ("pinned", [str(stock), "-c", "features.use_legacy_landlock=true", "app-server"]),
    ("experimental", [str(CANDIDATE / "codex"), "-c", "features.use_legacy_landlock=true", "app-server"]),
    ("runtime-launch", ["codex", "app-server"]),
]:
    home = ROOT / ("home-" + candidate)
    home.mkdir(mode=0o700)
    environment = dict(os.environ, CODEX_HOME=str(home))
    server = subprocess.Popen(command,
                              stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                              stderr=subprocess.DEVNULL, text=True, env=environment,
                              cwd=str(WORKSPACE))
    selector = selectors.DefaultSelector()
    selector.register(server.stdout, selectors.EVENT_READ)

    def send(value):
        server.stdin.write(json.dumps(value) + "\n")
        server.stdin.flush()

    def receive(identifier):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if not selector.select(max(0, deadline - time.monotonic())):
                break
            line = server.stdout.readline()
            if not line:
                raise RuntimeError("server ended before response")
            value = json.loads(line)
            if value.get("id") == identifier:
                return value
        raise TimeoutError("bounded response deadline")

    try:
        send({"id": 1, "method": "initialize", "params": {
            "clientInfo": {"name": "dataground-compatibility", "version": "0.1.0"},
            "capabilities": {"experimentalApi": True}}})
        if "error" in receive(1):
            raise RuntimeError("initialization rejected")
        send({"method": "initialized"})
        for identifier, mode in [(2, "readOnly"), (3, "workspaceWrite")]:
            policy = {"type": mode, "networkAccess": False}
            if mode == "workspaceWrite":
                policy.update(writableRoots=[str(WORKSPACE)], excludeTmpdirEnvVar=True,
                              excludeSlashTmp=True)
            send({"id": identifier, "method": "command/exec", "params": {
                "command": probe_command("api-" + candidate + "-" + mode),
                "cwd": str(WORKSPACE), "sandboxPolicy": policy,
                "timeoutMs": 10000, "outputBytesCap": 4096}})
            response = receive(identifier)
            result = response.get("result", {})
            observations = json.loads(result["stdout"]) if result.get("exitCode") == 0 else None
            record({"surface": "app-server-command", "candidate": candidate, "mode": mode,
                    "rpcError": response.get("error", {}).get("code"),
                    "exitCode": result.get("exitCode"),
                    "installationRejected": "SeccompInstall" in result.get("stderr", ""),
                    "observations": observations})
    finally:
        selector.close()
        server.terminate()
        try:
            server.wait(timeout=2)
        except subprocess.TimeoutExpired:
            server.kill()
            server.wait()

if len(records) != 10:
    raise RuntimeError("expected all native-helper and app-server comparisons")
for value in records:
    if value["candidate"] == "pinned":
        if value["exitCode"] != 101 or value["observations"] is not None or not value["installationRejected"]:
            raise RuntimeError("pinned native sandbox did not reproduce the known blocker")
    else:
        expected = {"workspaceWrite": "denied", "outsideWrite": "denied", "inetSocket": "denied",
                    "rawSocket": "denied", "userNamespace": "denied"}
        if value["mode"] in ["workspace-write", "workspaceWrite"]:
            expected["workspaceWrite"] = "allowed"
        if value["exitCode"] != 0 or value["observations"] != expected or value.get("rpcError") is not None:
            raise RuntimeError("candidate did not preserve native and outer restrictions")

# A parent adds a denial of PR_SET_SECCOMP; OpenShell already denies seccomp().
# If neither installation interface works, the candidate must not run the command.
BLOCK_INSTALLATION = r'''
import ctypes
import os
import platform
import sys

class Filter(ctypes.Structure):
    _fields_ = [("code", ctypes.c_ushort), ("jt", ctypes.c_ubyte),
                ("jf", ctypes.c_ubyte), ("k", ctypes.c_uint)]
class Program(ctypes.Structure):
    _fields_ = [("length", ctypes.c_ushort), ("filter", ctypes.POINTER(Filter))]
arch, syscall = {"aarch64": (0xc00000b7, 167), "x86_64": (0xc000003e, 157)}[platform.machine()]
instructions = (Filter * 9)(
    Filter(0x20, 0, 0, 4), Filter(0x15, 1, 0, arch), Filter(0x06, 0, 0, 0x80000000),
    Filter(0x20, 0, 0, 0), Filter(0x15, 0, 3, syscall), Filter(0x20, 0, 0, 16),
    Filter(0x15, 0, 1, 22), Filter(0x06, 0, 0, 0x00050001), Filter(0x06, 0, 0, 0x7fff0000))
program = Program(9, instructions)
lib = ctypes.CDLL(None, use_errno=True)
if lib.prctl(38, 1, 0, 0, 0) != 0 or lib.prctl(22, 2, ctypes.byref(program), 0, 0) != 0:
    raise RuntimeError("could not establish the negative control")
os.execv(sys.argv[1], sys.argv[2:])
'''
blocked = subprocess.run(["python3", "-c", BLOCK_INSTALLATION,
                          str(CANDIDATE / "codex-linux-sandbox"),
                          *helper_arguments("workspace-write", "must-not-execute")],
                         cwd=str(WORKSPACE), text=True, capture_output=True, timeout=15)
if blocked.returncode != 101 or "SeccompInstall" not in blocked.stderr or (WORKSPACE / "must-not-execute").exists():
    raise RuntimeError("candidate executed without installing its filter")
record({"surface": "native-helper", "candidate": "experimental", "mode": "both-interfaces-denied",
        "exitCode": blocked.returncode, "commandExecuted": False})
print("DATAGROUND_CODEX_COMPATIBILITY_OK", flush=True)
