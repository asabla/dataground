"""Check build-time substitutions before replacing the pinned native executable."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("activate_runtime", Path(__file__).with_name("activate-runtime.py"))
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


class RuntimeActivationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.expected = json.loads(Path(__file__).with_name("source.json").read_text())
        self.metadata = self.root / "usr/local/share/dataground-codex-compatibility"
        self.metadata.mkdir(parents=True)
        (self.metadata / "candidate-source.json").write_text(json.dumps(self.expected))
        self.candidate = self.root / "opt/dataground-compatibility"
        self.candidate.mkdir(parents=True)
        inventory = []
        for name in ["codex", "codex-linux-sandbox"]:
            content = ("candidate-" + name).encode()
            (self.candidate / name).write_bytes(content)
            inventory.append(hashlib.sha256(content).hexdigest() + "  target/debug/" + name)
        (self.metadata / "candidate-binaries.sha256").write_text("\n".join(inventory) + "\n")
        self.module = self.root / "usr/lib/node_modules/@openai/codex"
        self.executable = self.module / "node_modules/@openai/codex-linux-arm64/vendor/aarch64-unknown-linux-musl/codex/codex"
        self.executable.parent.mkdir(parents=True)
        self.executable.write_bytes(b"stock native executable")
        launcher = self.module / "bin/codex.js"
        launcher.parent.mkdir()
        launcher.write_text("stock launcher")
        (self.root / "usr/bin").mkdir()
        (self.root / "usr/bin/codex").symlink_to(launcher)

    def activate(self):
        runtime.activate(self.root, "aarch64", self.expected)

    def rejected(self):
        with self.assertRaises((RuntimeError, FileNotFoundError)):
            self.activate()
        self.assertEqual(self.executable.read_bytes(), b"stock native executable")
        self.assertFalse((self.candidate / "stock-codex").exists())
        self.assertFalse((self.metadata / "runtime-launch.json").exists())

    def test_preserves_launcher_and_stock_with_exact_runtime_metadata(self):
        self.activate()
        self.assertEqual((self.candidate / "stock-codex").read_bytes(), b"stock native executable")
        self.assertEqual(self.executable.read_bytes(), b"candidate-codex")
        self.assertEqual((self.module / "bin/codex.js").read_text(), "stock launcher")
        self.assertEqual(self.executable.stat().st_mode & 0o777, 0o755)
        config = self.root / "etc/codex/config.toml"
        self.assertEqual(config.read_text(), "[features]\nuse_legacy_landlock = true\n")
        self.assertEqual(config.stat().st_mode & 0o777, 0o444)
        metadata = json.loads((self.metadata / "runtime-launch.json").read_text())
        self.assertFalse(metadata["certificationEligible"])
        self.assertEqual(metadata["nativeExecutableSHA256"], runtime.digest(self.executable))
        self.assertEqual(metadata["systemConfigSHA256"], runtime.digest(config))
        with self.assertRaises(RuntimeError):
            self.activate()

    def test_rejects_binary_substitution_before_replacement(self):
        (self.candidate / "codex").write_bytes(b"substitution")
        self.rejected()

    def test_selects_the_existing_amd64_provider_path(self):
        executable = self.module / "node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/codex/codex"
        executable.parent.mkdir(parents=True)
        self.executable.rename(executable)
        runtime.activate(self.root, "x86_64", self.expected)
        self.assertEqual(executable.read_bytes(), b"candidate-codex")
        metadata = json.loads((self.metadata / "runtime-launch.json").read_text())
        self.assertEqual(metadata["nativeExecutable"], "/" + str(executable.relative_to(self.root)))

    def test_rejects_source_substitution_before_replacement(self):
        source = dict(self.expected, sourceCommit="0" * 40)
        (self.metadata / "candidate-source.json").write_text(json.dumps(source))
        self.rejected()

    def test_rejects_missing_helper_hash_before_replacement(self):
        inventory = self.metadata / "candidate-binaries.sha256"
        inventory.write_text(inventory.read_text().splitlines()[0] + "\n")
        self.rejected()

    def test_rejects_launcher_substitution_before_replacement(self):
        launcher = self.root / "usr/bin/codex"
        launcher.unlink()
        launcher.write_text("substitution")
        self.rejected()

    def test_rejects_existing_system_configuration_before_replacement(self):
        config = self.root / "etc/codex/config.toml"
        config.parent.mkdir(parents=True)
        config.write_text("existing = true\n")
        self.rejected()
        self.assertEqual(config.read_text(), "existing = true\n")


if __name__ == "__main__":
    unittest.main()
