#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/release.sh <version> [commit message]

Examples:
  ./scripts/release.sh v1.0.0
  ./scripts/release.sh v1.0.1 "fix: preserve pptx xml namespaces"

Behavior:
  1. Stage all changes
  2. Commit if there are changes
  3. Push the current branch
  4. Create an annotated tag
  5. Push the tag to origin

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
commit_message="${2:-release: ${version}}"

if git rev-parse --verify --quiet "${version}" >/dev/null; then
  echo "error: tag ${version} already exists" >&2
  exit 1
fi

branch="$(git rev-parse --abbrev-ref HEAD)"
if [[ "${branch}" == "HEAD" ]]; then
  echo "error: detached HEAD is not supported for release" >&2
  exit 1
fi

git add -A

if ! git diff --cached --quiet; then
  git commit -m "${commit_message}"
else
  echo "no staged changes to commit, continuing with existing HEAD"
fi

git push origin "${branch}"
git tag -a "${version}" -m "Release ${version}"
git push origin "${version}"

echo "release tag pushed: ${version}"
echo "GitHub Actions should now build and publish release artifacts."
