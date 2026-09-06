import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("publication", Path(__file__).with_name("publication.py"))
publication = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publication)

SOURCE_COMMIT = "1" * 40
IMAGE = "sha256:" + "2" * 64


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.TemporaryDirectory()
        self.addCleanup(self.home.cleanup)
        self.directory = Path(self.home.name)
        (self.directory / "candidate.tar").write_bytes(b"controlled synthetic archive")
        self.record = {
            "contract": publication.CONTRACT, "sourceCommit": SOURCE_COMMIT,
            "architecture": "arm64", "imageId": IMAGE,
            "archiveSHA256": publication.file_digest(self.directory / "candidate.tar"),
            "certificationEligible": False,
        }
        self.write_record()

    def write_record(self):
        (self.directory / "tested-image.json").write_text(json.dumps(self.record))

    def test_rejects_archive_substitution_before_docker(self):
        (self.directory / "candidate.tar").write_bytes(b"substituted")
        with patch.object(publication, "docker") as docker:
            with self.assertRaises(ValueError):
                publication.load(self.directory, SOURCE_COMMIT, "arm64")
            docker.assert_not_called()

    def test_rejects_cross_scope_and_certification_claims(self):
        for key, value in (("sourceCommit", "3" * 40), ("architecture", "amd64"),
                           ("certificationEligible", True), ("imageId", "candidate:latest"),
                           ("unexpected", "field")):
            with self.subTest(key=key), patch.object(publication, "docker") as docker:
                changed = dict(self.record)
                changed[key] = value
                (self.directory / "tested-image.json").write_text(json.dumps(changed))
                with self.assertRaises(ValueError):
                    publication.load(self.directory, SOURCE_COMMIT, "arm64")
                docker.assert_not_called()

    def test_rejects_symlink_archive(self):
        archive = self.directory / "candidate.tar"
        archive.rename(self.directory / "other.tar")
        archive.symlink_to(self.directory / "other.tar")
        with patch.object(publication, "docker") as docker:
            with self.assertRaises(OSError):
                publication.load(self.directory, SOURCE_COMMIT, "arm64")
            docker.assert_not_called()

    def test_loaded_image_must_match_checked_architecture_and_isolation(self):
        metadata = {"Id": IMAGE, "Architecture": "arm64", "Os": "linux", "Config": {
            "User": "sandbox", "Labels": {
                "dataground.dev.codex-compatibility-source": publication.SOURCE,
                "dataground.dev.certification-eligible": "false",
                "org.opencontainers.image.source": "https://github.com/asabla/dataground",
            },
        }}
        with patch.object(publication, "docker", return_value=json.dumps([metadata]).encode()) as docker:
            publication.load(self.directory, SOURCE_COMMIT, "arm64")
            self.assertEqual(docker.call_count, 2)
            metadata["Config"]["User"] = "root"
            docker.return_value = json.dumps([metadata]).encode()
            with self.assertRaises(ValueError):
                publication.load(self.directory, SOURCE_COMMIT, "arm64")

    def test_publish_rejects_injected_run_identity_before_any_effect(self):
        with patch.object(publication, "docker") as docker:
            with self.assertRaises(ValueError):
                publication.publish(self.directory, SOURCE_COMMIT, "arm64", "1\nother", "1")
            docker.assert_not_called()

    def test_prepare_rejects_dirty_or_different_checkout(self):
        for revision, dirty in ((SOURCE_COMMIT, b" M file"), ("4" * 40, b"")):
            with self.subTest(revision=revision, dirty=bool(dirty)):
                with patch.object(publication.subprocess, "check_output", side_effect=[revision, dirty]), patch.object(publication, "docker") as docker:
                    with self.assertRaises(ValueError):
                        publication.prepare(self.directory / "new", SOURCE_COMMIT, "arm64", IMAGE)
                    docker.assert_not_called()

    def test_publish_records_only_the_exact_repository_digest(self):
        metadata = {"RepoDigests": [publication.REPOSITORY + "@sha256:" + "5" * 64]}
        output = self.directory / "outputs"
        with patch.object(publication, "inspect", return_value=metadata), patch.object(publication, "docker") as docker, patch.dict(publication.os.environ, {"GITHUB_OUTPUT": str(output)}), patch("builtins.print"):
            publication.publish(self.directory, SOURCE_COMMIT, "arm64", "42", "1")
            self.assertEqual(output.read_text(), "digest=sha256:" + "5" * 64 + "\n")
            self.assertEqual(docker.call_args_list[0].args[:3], ("image", "tag", IMAGE))
            metadata["RepoDigests"].append(publication.REPOSITORY + "@sha256:" + "6" * 64)
            with self.assertRaises(ValueError):
                publication.publish(self.directory, SOURCE_COMMIT, "arm64", "42", "2")
            self.assertEqual(output.read_text().count("digest="), 1)


if __name__ == "__main__":
    unittest.main()
