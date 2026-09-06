"""Apply the exact reviewed compatibility patch to a clean pinned source tree."""

import hashlib
import json
from pathlib import Path
import subprocess
import sys


def prepare(root: Path, candidate: Path) -> None:
    profile = json.loads((candidate / "source.json").read_bytes())
    patch = candidate / "strict-file-rights.patch"
    if hashlib.sha256(patch.read_bytes()).hexdigest() != profile["patchSHA256"]:
        raise ValueError("candidate patch digest does not match")
    head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root).decode().strip()
    changes = subprocess.check_output(["git", "status", "--porcelain", "--untracked-files=all"], cwd=root)
    if head != profile["sourceCommit"] or changes:
        raise ValueError("candidate requires the exact clean upstream checkout")
    subprocess.run(["git", "apply", "--check", str(patch)], cwd=root, check=True)
    subprocess.run(["git", "apply", str(patch)], cwd=root, check=True)
    changed = subprocess.check_output(["git", "diff", "--name-only"], cwd=root).decode().splitlines()
    if changed != ["crates/openshell-supervisor-process/src/sandbox/linux/landlock.rs"]:
        raise ValueError("candidate changed an unexpected source file")
    (root / "candidate-source.json").write_text(json.dumps(profile, sort_keys=True) + "\n")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("usage: prepare-source.py <clean-upstream-checkout>")
    prepare(Path(sys.argv[1]).resolve(), Path(__file__).resolve().parent)
