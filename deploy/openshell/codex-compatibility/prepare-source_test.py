"""Exercise source preparation against a local clean checkout; no network is used."""
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import tomllib
import unittest

SOURCE = Path(sys.argv.pop(1)).resolve(strict=True)
INPUTS = Path(__file__).resolve().parent


class SourcePreparation(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.source = self.root / "source"
        self.inputs = self.root / "inputs"
        self.inputs.mkdir()
        subprocess.run(["git", "clone", "--quiet", "--shared", str(SOURCE), str(self.source)],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(["git", "remote", "set-url", "origin", "https://github.com/openai/codex.git"],
                       cwd=self.source, check=True)
        for name in ["Dockerfile", "source.json", "seccomp-prctl.patch", "prepare-source.py"]:
            shutil.copyfile(INPUTS / name, self.inputs / name)

    def prepare(self):
        return subprocess.run([sys.executable, str(self.inputs / "prepare-source.py"), str(self.source)],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode

    def metadata(self, name, value):
        path = self.inputs / "source.json"
        manifest = json.loads(path.read_text())
        manifest[name] = value
        path.write_text(json.dumps(manifest))

    def test_only_local_versions_and_reviewed_patch_change(self):
        old = tomllib.loads((self.source / "codex-rs/Cargo.lock").read_text())
        self.assertEqual(self.prepare(), 0)
        new = tomllib.loads((self.source / "codex-rs/Cargo.lock").read_text())
        self.assertEqual([entry for entry in old["package"] if "source" in entry],
                         [entry for entry in new["package"] if "source" in entry])
        changed = subprocess.check_output(["git", "diff", "--name-only"], cwd=self.source, text=True)
        self.assertEqual(changed.splitlines(), ["codex-rs/Cargo.lock", "codex-rs/linux-sandbox/src/landlock.rs"])
        self.assertIn("PR_SET_SECCOMP", (self.source / "codex-rs/linux-sandbox/src/landlock.rs").read_text())
        self.assertFalse(json.loads((self.source / "candidate-source.json").read_text())["certificationEligible"])
        self.assertNotEqual(self.prepare(), 0, "preparation must reject an already modified checkout")

    def test_source_substitution_fails(self):
        self.metadata("sourceCommit", "0" * 40)
        self.assertNotEqual(self.prepare(), 0)

    def test_patch_substitution_fails(self):
        with (self.inputs / "seccomp-prctl.patch").open("a") as patch:
            patch.write("\nsubstituted patch\n")
        self.assertNotEqual(self.prepare(), 0)

    def test_dirty_dependency_lock_fails(self):
        with (self.source / "codex-rs/Cargo.lock").open("a") as lock:
            lock.write("\n# unreviewed change\n")
        self.assertNotEqual(self.prepare(), 0)

    def test_certification_claim_fails(self):
        self.metadata("certificationEligible", True)
        self.assertNotEqual(self.prepare(), 0)

    def test_build_image_substitution_fails(self):
        self.metadata("baseImage", "unreviewed:latest")
        self.assertNotEqual(self.prepare(), 0)

    def test_version_substitution_fails(self):
        self.metadata("sourceVersion", "0.118.0")
        self.assertNotEqual(self.prepare(), 0)

    def test_repository_substitution_fails(self):
        subprocess.run(["git", "remote", "set-url", "origin", "https://example.invalid/unreviewed.git"],
                       cwd=self.source, check=True)
        self.assertNotEqual(self.prepare(), 0)


unittest.main()
