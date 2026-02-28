#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/release.sh <version>

Examples:
  ./scripts/release.sh v1.0.0

Behavior:
  1. Verify the current branch
  2. Push the current branch HEAD
  3. Create an annotated tag from the current HEAD commit
  4. Push the tag to origin

The GitHub Actions workflow will build release binaries after the tag is pushed.
EOF
}

if [[ $# -lt 1 ]]; then
  usage
  exit 1
fi

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "error: current directory is not a git repository" >&2
  exit 1
fi

version="$1"

if git rev-parse --verify --quiet "${version}" >/dev/null; then
  echo "error: tag ${version} already exists" >&2
  exit 1
fi

branch="$(git rev-parse --abbrev-ref HEAD)"
if [[ "${branch}" == "HEAD" ]]; then
  echo "error: detached HEAD is not supported for release" >&2
  exit 1
fi

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "warning: working tree has uncommitted changes; release will use the current committed HEAD only"
fi

git push origin "${branch}"
git tag -a "${version}" -m "Release ${version}"
git push origin "${version}"

echo "release tag pushed: ${version}"
echo "GitHub Actions should now build and publish release artifacts."
