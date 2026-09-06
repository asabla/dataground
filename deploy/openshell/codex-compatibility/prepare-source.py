"""Prepare only the reviewed source patch and release-local lock version alignment."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tomllib

root = Path(sys.argv[1]).resolve(strict=True)
inputs = Path(__file__).resolve().parent
manifest = json.loads((inputs / "source.json").read_text())
if manifest["certificationEligible"] is not False:
    raise RuntimeError("a compatibility build cannot claim certification")
dockerfile = (inputs / "Dockerfile").read_text()
for image in [manifest["rustImage"], manifest["baseImage"]]:
    if "FROM " + image not in dockerfile:
        raise RuntimeError("candidate build image differs from source metadata")
remote = subprocess.check_output(["git", "remote", "get-url", "origin"], cwd=root, text=True).strip()
if remote != manifest["sourceRepository"]:
    raise RuntimeError("unexpected candidate source repository")
commit = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
if commit != manifest["sourceCommit"]:
    raise RuntimeError("source commit does not match the candidate")
if subprocess.check_output(["git", "status", "--porcelain"], cwd=root):
    raise RuntimeError("candidate source must be clean before preparation")
patch = inputs / "seccomp-prctl.patch"
if hashlib.sha256(patch.read_bytes()).hexdigest() != manifest["patchSHA256"]:
    raise RuntimeError("candidate patch digest mismatch")
workspace = tomllib.loads((root / "codex-rs/Cargo.toml").read_text())
if workspace["workspace"]["package"]["version"] != manifest["sourceVersion"]:
    raise RuntimeError("source version does not match the candidate")
lock_path = root / "codex-rs/Cargo.lock"
original = lock_path.read_text()
old_lock = tomllib.loads(original)
parts = original.split("[[package]]")
changed = 0
for index, part in enumerate(parts):
    if 'source = ' not in part and '\nversion = "0.0.0"\n' in part:
        parts[index] = part.replace('\nversion = "0.0.0"\n', '\nversion = "' + manifest["sourceVersion"] + '"\n', 1)
        changed += 1
updated = "[[package]]".join(parts)
new_lock = tomllib.loads(updated)
external = lambda lock: [package for package in lock["package"] if "source" in package]
if changed != 87 or len(external(old_lock)) != 995 or external(old_lock) != external(new_lock):
    raise RuntimeError("unexpected release lock alignment or external dependency change")
subprocess.run(["git", "apply", "--check", str(patch)], cwd=root, check=True)
subprocess.run(["git", "apply", str(patch)], cwd=root, check=True)
lock_path.write_text(updated)
manifest["originalCargoLockSHA256"] = hashlib.sha256(original.encode()).hexdigest()
manifest["preparedCargoLockSHA256"] = hashlib.sha256(updated.encode()).hexdigest()
(root / "candidate-source.json").write_text(json.dumps(manifest, indent=2) + "\n")
