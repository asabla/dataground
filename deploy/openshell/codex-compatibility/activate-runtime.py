"""Install the reviewed candidate at the existing provider-authorized Codex path."""
import hashlib
import json
from pathlib import Path
import platform
import re
import shutil
import sys


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def activate(root, architecture, expected):
    root = root.resolve(strict=True)
    candidate = root / "opt/dataground-compatibility"
    metadata = root / "usr/local/share/dataground-codex-compatibility"
    source = json.loads((metadata / "candidate-source.json").read_text())
    if expected.get("certificationEligible") is not False or any(source.get(key) != value for key, value in expected.items()):
        raise RuntimeError("candidate source metadata differs from the reviewed build")
    hashes = {}
    for line in (metadata / "candidate-binaries.sha256").read_text().splitlines():
        match = re.fullmatch(r"([a-f0-9]{64})  target/debug/(codex|codex-linux-sandbox)", line)
        if match is None or match[2] in hashes:
            raise RuntimeError("invalid candidate binary inventory")
        hashes[match[2]] = match[1]
    if set(hashes) != {"codex", "codex-linux-sandbox"} or any(
        (candidate / name).is_symlink() or digest(candidate / name) != value
        for name, value in hashes.items()
    ):
        raise RuntimeError("candidate binary digest mismatch")
    package, triple = {
        "aarch64": ("codex-linux-arm64", "aarch64-unknown-linux-musl"),
        "x86_64": ("codex-linux-x64", "x86_64-unknown-linux-musl"),
    }[architecture]
    module = root / "usr/lib/node_modules/@openai/codex"
    executable = module / "node_modules/@openai" / package / "vendor" / triple / "codex/codex"
    launcher = root / "usr/bin/codex"
    config = root / "etc/codex/config.toml"
    stock = candidate / "stock-codex"
    if (launcher.resolve(strict=True) != module / "bin/codex.js"
            or executable.resolve(strict=True) != executable or not executable.is_file()
            or config.exists() or config.is_symlink() or stock.exists() or stock.is_symlink()
            or (metadata / "runtime-launch.json").exists()):
        raise RuntimeError("unexpected or already activated runtime layout")
    # All substitutions are validated before changing the image. Preserve the
    # original ELF for comparison; the npm launcher and provider paths stay fixed.
    stock_hash = digest(executable)
    shutil.copy2(executable, stock)
    shutil.copyfile(candidate / "codex", executable)
    executable.chmod(0o755)
    config.parent.mkdir(parents=True, exist_ok=True)
    config.write_text("[features]\nuse_legacy_landlock = true\n")
    config.chmod(0o444)
    (metadata / "runtime-launch.json").write_text(json.dumps({
        "certificationEligible": False,
        "nativeExecutable": "/" + str(executable.relative_to(root)),
        "nativeExecutableSHA256": hashes["codex"],
        "stockExecutableSHA256": stock_hash,
        "systemConfigSHA256": digest(config),
        "command": "codex app-server",
    }, indent=2) + "\n")


if __name__ == "__main__":
    activate(Path("/"), platform.machine(), json.loads(Path(sys.argv[1]).read_text()))
