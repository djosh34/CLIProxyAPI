#!/usr/bin/env python3
"""Prints, as a JSON array, the upstream vX.Y.Z tags that need a scratch image.

These are the upstream tags newer than the newest vX.Y.Z tag in GHCR, or only
the newest upstream tag when GHCR has none yet.

Environment: IMAGE (e.g. djosh34/cliproxyapi), UPSTREAM (git URL), and
GHCR_USER and GHCR_TOKEN, required until the package exists and is public.
"""

import base64
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

VERSION_TAG = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")


def version(tag):
    match = VERSION_TAG.match(tag)
    return tuple(int(part) for part in match.groups()) if match else None


def upstream_tags(url):
    out = subprocess.run(
        ["git", "ls-remote", "--tags", "--refs", url],
        check=True,
        capture_output=True,
        text=True,
    ).stdout
    return [line.split("refs/tags/", 1)[1] for line in out.splitlines()]


def registry_token(image):
    query = urllib.parse.urlencode({"service": "ghcr.io", "scope": f"repository:{image}:pull"})
    request = urllib.request.Request(f"https://ghcr.io/token?{query}")
    user, password = os.environ.get("GHCR_USER"), os.environ.get("GHCR_TOKEN")
    if user and password:
        basic = base64.b64encode(f"{user}:{password}".encode()).decode()
        request.add_header("Authorization", f"Basic {basic}")
    with urllib.request.urlopen(request) as response:
        return json.load(response)["token"]


def published_tags(image):
    """Returns the image's tags, or an empty list when the package does not exist."""
    token = registry_token(image)
    tags = []
    path = f"/v2/{image}/tags/list?n=1000"
    while path:
        request = urllib.request.Request(f"https://ghcr.io{path}")
        request.add_header("Authorization", f"Bearer {token}")
        try:
            with urllib.request.urlopen(request) as response:
                tags += json.load(response).get("tags") or []
                link = response.headers.get("Link", "")
        except urllib.error.HTTPError as err:
            body = err.read().decode(errors="replace")
            if err.code == 404 and "NAME_UNKNOWN" in body:
                return []
            raise RuntimeError(f"GHCR tags/list returned {err.code}: {body}") from err
        match = re.match(r"<([^>]+)>", link)
        path = match.group(1) if match else None
    return tags


def main():
    image, upstream = os.environ["IMAGE"], os.environ["UPSTREAM"]
    candidates = sorted((t for t in upstream_tags(upstream) if version(t)), key=version)
    if not candidates:
        raise RuntimeError(f"no vX.Y.Z tags found in {upstream}")
    published = [version(t) for t in published_tags(image) if version(t)]
    if published:
        newest = max(published)
        build = [t for t in candidates if version(t) > newest]
    else:
        build = candidates[-1:]
    print(json.dumps(build))


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.CalledProcessError, KeyError, ValueError) as err:
        print(f"error: {err}", file=sys.stderr)
        sys.exit(1)
