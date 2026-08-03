#!/usr/bin/env python3
"""Recompute the three custom metadata keys the Hangar reader requires.

A fake-gcs restart dropped custom metadata on 16 of 21 objects, so every read
fails validateStoredMetadata's `len(metadata) != 3` check as ErrCorrupt. The
bytes survived, and all three keys are derivable, so this repairs rather than
prunes:

  concourse-representation      -> the constant "zstd"
  concourse-uncompressed-sha256 -> the object key already carries the digest
  concourse-uncompressed-bytes  -> recomputed by decompressing

The digest recomputed from the decompressed bytes must equal the one in the
key. That equality is the whole safety argument: it proves the content is the
content the platform believes it stored, so writing metadata back asserts
nothing that was not verified. An object that fails it is genuinely corrupt and
is reported, never patched and never deleted.

Usage: hangar-repair.py [--apply]   (default is a dry run)
"""
import json
import subprocess
import sys
import urllib.request
import hashlib

BASE = "http://127.0.0.1:14443/storage/v1/b/concourse-hangar/o"
DOWNLOAD = "http://127.0.0.1:14443/download/storage/v1/b/concourse-hangar/o"
APPLY = "--apply" in sys.argv

REPRESENTATION = "concourse-representation"
SHA256 = "concourse-uncompressed-sha256"
BYTES = "concourse-uncompressed-bytes"


def listing():
    with urllib.request.urlopen(f"{BASE}?maxResults=1000") as r:
        return json.load(r).get("items", [])


def fetch(name):
    quoted = urllib.parse.quote(name, safe="")
    with urllib.request.urlopen(f"{DOWNLOAD}/{quoted}?alt=media") as r:
        return r.read()


def digest_from_key(name):
    # hangar/v1/<kind>/sha256/<hex>.tar.zst
    tail = name.rsplit("/", 1)[-1]
    return "sha256:" + tail.split(".", 1)[0]


def patch(name, metadata):
    quoted = urllib.parse.quote(name, safe="")
    body = json.dumps({"metadata": metadata}).encode()
    req = urllib.request.Request(
        f"{BASE}/{quoted}", data=body, method="PATCH",
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req) as r:
        return r.status


repaired, healthy, broken = [], [], []
for obj in listing():
    name = obj["name"]
    md = obj.get("metadata") or {}
    if sorted(md.keys()) == sorted([REPRESENTATION, SHA256, BYTES]):
        healthy.append(name)
        continue

    want_digest = digest_from_key(name)
    try:
        compressed = fetch(name)
        plain = subprocess.run(
            ["zstd", "-d", "-c"], input=compressed,
            capture_output=True, check=True,
        ).stdout
    except Exception as exc:  # noqa: BLE001 - report, never guess
        broken.append((name, f"decompress failed: {exc}"))
        continue

    actual = "sha256:" + hashlib.sha256(plain).hexdigest()
    if actual != want_digest:
        broken.append((name, f"content digest {actual} != key {want_digest}"))
        continue

    metadata = {
        REPRESENTATION: "zstd",
        SHA256: want_digest,
        BYTES: str(len(plain)),
    }
    if APPLY:
        patch(name, metadata)
    repaired.append((name, len(plain)))

print(f"already healthy : {len(healthy)}")
print(f"{'repaired' if APPLY else 'repairable'}: {len(repaired)}")
for name, size in repaired:
    print(f"    {name.rsplit('/',1)[-1][:24]}…  uncompressed={size}")
print(f"genuinely corrupt: {len(broken)}")
for name, why in broken:
    print(f"    {name.rsplit('/',1)[-1][:24]}…  {why}")
if not APPLY:
    print("\n(dry run - rerun with --apply to write metadata back)")
