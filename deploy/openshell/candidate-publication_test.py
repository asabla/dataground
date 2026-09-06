import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("publication", Path(__file__).with_name("candidate-publication.py"))
publication = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publication)

SOURCE_COMMIT = "1" * 40
IMAGE = "sha256:" + "2" * 64


class PublicationTests(unittest.TestCase):
    candidate = "codex"

    def setUp(self):
        self.contract, self.repository, self.user, _, self.labels = publication.candidate_profile(self.candidate)
        self.home = tempfile.TemporaryDirectory()
        self.addCleanup(self.home.cleanup)
        self.directory = Path(self.home.name)
        (self.directory / "candidate.tar").write_bytes(b"controlled synthetic archive")
        self.record = {
            "contract": self.contract, "sourceCommit": SOURCE_COMMIT,
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
                publication.load(self.directory, SOURCE_COMMIT, "arm64", self.candidate)

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
                    publication.load(self.directory, SOURCE_COMMIT, "arm64", self.candidate)
                docker.assert_not_called()

    def test_rejects_other_candidate_contract_before_publication(self):
        other = "supervisor" if self.candidate == "codex" else "codex"
        self.record["contract"] = publication.candidate_profile(other)[0]
        self.write_record()
        with patch.object(publication, "docker") as docker:
            with self.assertRaises(ValueError):
                publication.publish(self.directory, SOURCE_COMMIT, "arm64", "42", "1", self.candidate)
            docker.assert_not_called()

    def test_rejects_symlink_archive(self):
        archive = self.directory / "candidate.tar"
        archive.rename(self.directory / "other.tar")
        archive.symlink_to(self.directory / "other.tar")
        with patch.object(publication, "docker") as docker:
            with self.assertRaises(OSError):
                publication.load(self.directory, SOURCE_COMMIT, "arm64", self.candidate)
            docker.assert_not_called()

    def test_loaded_image_must_match_checked_architecture_and_isolation(self):
        metadata = {"Id": IMAGE, "Architecture": "arm64", "Os": "linux", "Config": {
            "User": self.user, "Labels": {
                **self.labels,
                "dataground.dev.certification-eligible": "false",
                "org.opencontainers.image.source": "https://github.com/asabla/dataground",
            },
        }}
        with patch.object(publication, "docker", return_value=json.dumps([metadata]).encode()) as docker:
            publication.load(self.directory, SOURCE_COMMIT, "arm64", self.candidate)
            self.assertEqual(docker.call_count, 2)
            metadata["Config"]["User"] = "root"
            docker.return_value = json.dumps([metadata]).encode()
            with self.assertRaises(ValueError):
                publication.load(self.directory, SOURCE_COMMIT, "arm64", self.candidate)

            # Containerd omits an unset User; the supervisor preserves that
            # upstream default, while the workload image must name sandbox.
            del metadata["Config"]["User"]
            docker.return_value = json.dumps([metadata]).encode()
            if self.candidate == "supervisor":
                publication.load(self.directory, SOURCE_COMMIT, "arm64", self.candidate)
            else:
                with self.assertRaises(ValueError):
                    publication.load(self.directory, SOURCE_COMMIT, "arm64", self.candidate)
            metadata["Config"]["User"] = None
            docker.return_value = json.dumps([metadata]).encode()
            with self.assertRaises(ValueError):
                publication.load(self.directory, SOURCE_COMMIT, "arm64", self.candidate)

    def test_publish_rejects_injected_run_identity_before_any_effect(self):
        with patch.object(publication, "docker") as docker:
            with self.assertRaises(ValueError):
                publication.publish(self.directory, SOURCE_COMMIT, "arm64", "1\nother", "1", self.candidate)
            docker.assert_not_called()

    def test_prepare_rejects_dirty_or_different_checkout(self):
        for revision, dirty in ((SOURCE_COMMIT, b" M file"), ("4" * 40, b"")):
            with self.subTest(revision=revision, dirty=bool(dirty)):
                with patch.object(publication.subprocess, "check_output", side_effect=[revision, dirty]), patch.object(publication, "docker") as docker:
                    with self.assertRaises(ValueError):
                        publication.prepare(self.directory / "new", SOURCE_COMMIT, "arm64", IMAGE, self.candidate)
                    docker.assert_not_called()

    def test_publish_records_only_the_exact_repository_digest(self):
        metadata = {"RepoDigests": [self.repository + "@sha256:" + "5" * 64]}
        output = self.directory / "outputs"
        with patch.object(publication, "inspect", return_value=metadata), patch.object(publication, "docker") as docker, patch.dict(publication.os.environ, {"GITHUB_OUTPUT": str(output)}), patch("builtins.print"):
            publication.publish(self.directory, SOURCE_COMMIT, "arm64", "42", "1", self.candidate)
            self.assertEqual(output.read_text(), "digest=sha256:" + "5" * 64 + "\n")
            self.assertEqual(docker.call_args_list[0].args[:3], ("image", "tag", IMAGE))
            metadata["RepoDigests"].append(self.repository + "@sha256:" + "6" * 64)
            with self.assertRaises(ValueError):
                publication.publish(self.directory, SOURCE_COMMIT, "arm64", "42", "2", self.candidate)
            self.assertEqual(output.read_text().count("digest="), 1)


class SupervisorPublicationTests(PublicationTests):
    candidate = "supervisor"

    def test_supervisor_requires_exact_source_patch_and_privilege_metadata(self):
        metadata = {"Id": IMAGE, "Architecture": "arm64", "Os": "linux", "Config": {
            "User": self.user, "Labels": {**self.labels,
                "dataground.dev.certification-eligible": "false",
                "org.opencontainers.image.source": "https://github.com/asabla/dataground",
            },
        }}
        for field in self.labels:
            with self.subTest(field=field):
                changed = json.loads(json.dumps(metadata))
                changed["Config"]["Labels"][field] = "substituted"
                with patch.object(publication, "docker", return_value=json.dumps([changed]).encode()):
                    with self.assertRaises(ValueError):
                        publication.inspect(IMAGE, "arm64", self.candidate)
        with patch.object(publication, "docker") as docker:
            for architecture, candidate in (("amd64", self.candidate), ("arm64", "unknown")):
                with self.assertRaises(ValueError):
                    publication.inspect(IMAGE, architecture, candidate)
            docker.assert_not_called()


if __name__ == "__main__":
    unittest.main()
