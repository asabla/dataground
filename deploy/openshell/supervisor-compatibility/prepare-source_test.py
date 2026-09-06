"""Exercise preparation against disposable copies of the exact upstream source."""

import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import importlib.util

candidate = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("candidate_prepare", candidate / "prepare-source.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
prepare = module.prepare
upstream = Path(sys.argv.pop(1)).resolve()


class PreparationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="supervisor-source-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name) / "source"
        self.candidate = Path(self.temporary.name) / "candidate"
        shutil.copytree(candidate, self.candidate)
        subprocess.run(["git", "clone", "--quiet", "--shared", "--no-hardlinks", str(upstream), str(self.root)], check=True)

    def test_exact_source_applies_once_and_preserves_dependencies(self):
        lock = (self.root / "Cargo.lock").read_bytes()
        prepare(self.root, self.candidate)
        self.assertEqual(lock, (self.root / "Cargo.lock").read_bytes())
        self.assertEqual(json.loads((self.root / "candidate-source.json").read_bytes()), json.loads((self.candidate / "source.json").read_bytes()))
        with self.assertRaises(ValueError):
            prepare(self.root, self.candidate)

    def test_tampered_patch_fails_before_source_mutation(self):
        with (self.candidate / "strict-file-rights.patch").open("ab") as stream:
            stream.write(b"\n")
        with self.assertRaises(ValueError):
            prepare(self.root, self.candidate)
        self.assertEqual(subprocess.check_output(["git", "status", "--porcelain"], cwd=self.root), b"")

    def test_dirty_source_and_wrong_revision_fail_before_patch(self):
        (self.root / "unexpected").write_text("dirty")
        with self.assertRaises(ValueError):
            prepare(self.root, self.candidate)
        (self.root / "unexpected").unlink()
        profile = json.loads((self.candidate / "source.json").read_bytes())
        profile["sourceCommit"] = "0" * 40
        (self.candidate / "source.json").write_text(json.dumps(profile))
        with self.assertRaises(ValueError):
            prepare(self.root, self.candidate)
        self.assertEqual(subprocess.check_output(["git", "diff"], cwd=self.root), b"")

    def test_build_inputs_retain_exact_pins(self):
        profile = json.loads((self.candidate / "source.json").read_bytes())
        dockerfile = (self.candidate / "Dockerfile").read_text()
        self.assertEqual(hashlib.sha256((self.candidate / "strict-file-rights.patch").read_bytes()).hexdigest(), profile["patchSHA256"])
        for value in (profile["rustImage"], profile["baseImage"], profile["sourceRepository"], profile["sourceCommit"], profile["patchSHA256"]):
            self.assertIn(value, dockerfile)
        self.assertIs(profile["certificationEligible"], False)


if __name__ == "__main__":
    unittest.main()
