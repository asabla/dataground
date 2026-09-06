"""Transfer an exact tested candidate between isolated build/publication jobs."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess

CONTRACT = "dataground.codex-candidate-publication/v1"
REPOSITORY = "ghcr.io/asabla/dataground-codex-candidate"
SOURCE = "4c70bff480af37b1bf1a9b352b8341060fe55755"
DIGEST = re.compile(r"sha256:[a-f0-9]{64}\Z")
REVISION = re.compile(r"[a-f0-9]{40}\Z")


def docker(*args):
    result = subprocess.run(["docker", *args], check=True, stdout=subprocess.PIPE)
    return result.stdout


def candidate_profile(candidate):
    if candidate == "codex":
        return CONTRACT, REPOSITORY, "sandbox", ("amd64", "arm64"), {
            "dataground.dev.codex-compatibility-source": SOURCE,
        }
    if candidate == "supervisor":
        return "dataground.supervisor-candidate-publication/v1", "ghcr.io/asabla/dataground-supervisor-candidate", "", ("arm64",), {
            "dataground.dev.supervisor-compatibility-source": "d556748771c41cbbd4e4dd7cd9030c798afe2b7d",
            "dataground.dev.supervisor-compatibility-patch": "5e97724dd9d9e7fad9abed8a46b9a4d6e06979119998c411daf34b2423056057",
        }
    raise ValueError("unknown candidate profile")


def inspect(image, architecture, candidate="codex"):
    _, _, user, architectures, required_labels = candidate_profile(candidate)
    if architecture not in architectures or not DIGEST.fullmatch(image):
        raise ValueError("unsupported candidate architecture or image")
    values = json.loads(docker("image", "inspect", image))
    if not isinstance(values, list) or len(values) != 1:
        raise ValueError("ambiguous candidate image")
    value = values[0]
    labels = value.get("Config", {}).get("Labels", {})
    if (value.get("Id") != image or value.get("Architecture") != architecture
            or value.get("Os") != "linux" or value.get("Config", {}).get("User", "") != user
            or any(labels.get(key) != expected for key, expected in required_labels.items())
            or labels.get("dataground.dev.certification-eligible") != "false"
            or labels.get("org.opencontainers.image.source") != "https://github.com/asabla/dataground"):
        raise ValueError("candidate image identity or isolation metadata changed")
    return value


def file_digest(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= 8 << 30:
            raise ValueError("invalid candidate archive")
        digest = hashlib.file_digest(source, "sha256").hexdigest()
        after = os.fstat(source.fileno())
        if (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns) != (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns):
            raise ValueError("candidate archive changed during verification")
        return digest


def checked_record(directory, source_commit, architecture, candidate="codex"):
    contract, _, _, architectures, _ = candidate_profile(candidate)
    if not REVISION.fullmatch(source_commit) or architecture not in architectures:
        raise ValueError("invalid candidate publication scope")
    path = directory / "tested-image.json"
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= 4096:
            raise ValueError("invalid candidate record")
        record = json.load(source)
    if (not isinstance(record, dict) or set(record) != {
            "contract", "sourceCommit", "architecture", "imageId", "archiveSHA256", "certificationEligible"}
            or record["contract"] != contract or record["sourceCommit"] != source_commit
            or record["architecture"] != architecture or record["certificationEligible"] is not False
            or not isinstance(record["imageId"], str) or not DIGEST.fullmatch(record["imageId"])
            or not isinstance(record["archiveSHA256"], str)
            or record["archiveSHA256"] != file_digest(directory / "candidate.tar")):
        raise ValueError("candidate record or archive does not match the successful build")
    return record


def prepare(directory, source_commit, architecture, image, candidate="codex"):
    contract, _, _, architectures, _ = candidate_profile(candidate)
    if not REVISION.fullmatch(source_commit) or not DIGEST.fullmatch(image) or architecture not in architectures:
        raise ValueError("exact source and image are required")
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    dirty = subprocess.check_output(["git", "status", "--porcelain", "--untracked-files=all"])
    if revision != source_commit or dirty:
        raise ValueError("candidate publication requires the exact clean checkout")
    inspect(image, architecture, candidate)
    directory.mkdir(mode=0o700)
    docker("image", "save", "--output", str(directory / "candidate.tar"), image)
    record = {
        "contract": contract, "sourceCommit": source_commit, "architecture": architecture,
        "imageId": image, "archiveSHA256": file_digest(directory / "candidate.tar"),
        "certificationEligible": False,
    }
    (directory / "tested-image.json").write_text(json.dumps(record, sort_keys=True) + "\n")


def load(directory, source_commit, architecture, candidate="codex"):
    record = checked_record(directory, source_commit, architecture, candidate)
    docker("image", "load", "--input", str(directory / "candidate.tar"))
    inspect(record["imageId"], architecture, candidate)


def publish(directory, source_commit, architecture, run_id, run_attempt, candidate="codex"):
    _, repository, _, _, _ = candidate_profile(candidate)
    if not re.fullmatch(r"[1-9][0-9]{0,19}", run_id) or not re.fullmatch(r"[1-9][0-9]{0,8}", run_attempt):
        raise ValueError("invalid publication run identity")
    record = checked_record(directory, source_commit, architecture, candidate)
    image = record["imageId"]
    inspect(image, architecture, candidate)
    tag = f"{repository}:candidate-{source_commit}-{architecture}-{run_id}-{run_attempt}"
    docker("image", "tag", image, tag)
    docker("image", "push", tag)
    value = inspect(image, architecture, candidate)
    digests = [entry.removeprefix(repository + "@") for entry in value.get("RepoDigests", [])
               if entry.startswith(repository + "@")]
    if len(digests) != 1 or not DIGEST.fullmatch(digests[0]):
        raise ValueError("published candidate digest is missing or ambiguous")
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf8") as output:
        output.write(f"digest={digests[0]}\n")
    print(f"Published experimental candidate {repository}@{digests[0]}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("prepare", "load", "publish"))
    parser.add_argument("--directory", required=True, type=Path)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--architecture", required=True, choices=("amd64", "arm64"))
    parser.add_argument("--candidate", choices=("codex", "supervisor"), default="codex")
    parser.add_argument("--image")
    parser.add_argument("--run-id")
    parser.add_argument("--run-attempt")
    args = parser.parse_args()
    if args.operation == "prepare":
        prepare(args.directory, args.source_commit, args.architecture, args.image or "", args.candidate)
    elif args.operation == "load":
        load(args.directory, args.source_commit, args.architecture, args.candidate)
    else:
        publish(args.directory, args.source_commit, args.architecture, args.run_id or "", args.run_attempt or "", args.candidate)


if __name__ == "__main__":
    main()
