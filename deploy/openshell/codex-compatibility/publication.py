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


def inspect(image, architecture):
    values = json.loads(docker("image", "inspect", image))
    if not isinstance(values, list) or len(values) != 1:
        raise ValueError("ambiguous candidate image")
    value = values[0]
    labels = value.get("Config", {}).get("Labels", {})
    if (value.get("Id") != image or value.get("Architecture") != architecture
            or value.get("Os") != "linux" or value.get("Config", {}).get("User") != "sandbox"
            or labels.get("dataground.dev.codex-compatibility-source") != SOURCE
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


def checked_record(directory, source_commit, architecture):
    if not REVISION.fullmatch(source_commit) or architecture not in ("amd64", "arm64"):
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
            or record["contract"] != CONTRACT or record["sourceCommit"] != source_commit
            or record["architecture"] != architecture or record["certificationEligible"] is not False
            or not isinstance(record["imageId"], str) or not DIGEST.fullmatch(record["imageId"])
            or not isinstance(record["archiveSHA256"], str)
            or record["archiveSHA256"] != file_digest(directory / "candidate.tar")):
        raise ValueError("candidate record or archive does not match the successful build")
    return record


def prepare(directory, source_commit, architecture, image):
    if not REVISION.fullmatch(source_commit) or not DIGEST.fullmatch(image):
        raise ValueError("exact source and image are required")
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    dirty = subprocess.check_output(["git", "status", "--porcelain", "--untracked-files=all"])
    if revision != source_commit or dirty:
        raise ValueError("candidate publication requires the exact clean checkout")
    inspect(image, architecture)
    directory.mkdir(mode=0o700)
    docker("image", "save", "--output", str(directory / "candidate.tar"), image)
    record = {
        "contract": CONTRACT, "sourceCommit": source_commit, "architecture": architecture,
        "imageId": image, "archiveSHA256": file_digest(directory / "candidate.tar"),
        "certificationEligible": False,
    }
    (directory / "tested-image.json").write_text(json.dumps(record, sort_keys=True) + "\n")


def load(directory, source_commit, architecture):
    record = checked_record(directory, source_commit, architecture)
    docker("image", "load", "--input", str(directory / "candidate.tar"))
    inspect(record["imageId"], architecture)


def publish(directory, source_commit, architecture, run_id, run_attempt):
    if not re.fullmatch(r"[1-9][0-9]{0,19}", run_id) or not re.fullmatch(r"[1-9][0-9]{0,8}", run_attempt):
        raise ValueError("invalid publication run identity")
    record = checked_record(directory, source_commit, architecture)
    image = record["imageId"]
    inspect(image, architecture)
    tag = f"{REPOSITORY}:candidate-{source_commit}-{architecture}-{run_id}-{run_attempt}"
    docker("image", "tag", image, tag)
    docker("image", "push", tag)
    value = inspect(image, architecture)
    digests = [entry.removeprefix(REPOSITORY + "@") for entry in value.get("RepoDigests", [])
               if entry.startswith(REPOSITORY + "@")]
    if len(digests) != 1 or not DIGEST.fullmatch(digests[0]):
        raise ValueError("published candidate digest is missing or ambiguous")
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf8") as output:
        output.write(f"digest={digests[0]}\n")
    print(f"Published experimental candidate {REPOSITORY}@{digests[0]}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("prepare", "load", "publish"))
    parser.add_argument("--directory", required=True, type=Path)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--architecture", required=True, choices=("amd64", "arm64"))
    parser.add_argument("--image")
    parser.add_argument("--run-id")
    parser.add_argument("--run-attempt")
    args = parser.parse_args()
    if args.operation == "prepare":
        prepare(args.directory, args.source_commit, args.architecture, args.image or "")
    elif args.operation == "load":
        load(args.directory, args.source_commit, args.architecture)
    else:
        publish(args.directory, args.source_commit, args.architecture, args.run_id or "", args.run_attempt or "")


if __name__ == "__main__":
    main()
