#!/usr/bin/env bash
# Shared by panghuli-ci.yml and panghuli-release.yml so a PR builds exactly what a merge
# publishes. Writes tag/previous/web_version/commit/built_at to $GITHUB_OUTPUT and unpacks
# the pinned front-end into public/dist.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# Tag = latest upstream release tag reachable from HEAD + "-panghuli.N"; N restarts at 1 for
# each upstream version. Needs full history and tags (checkout with fetch-depth: 0).
base=$(git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-panghuli.*' HEAD)
last=$(git tag -l "${base}-panghuli.*" | sed 's/.*-panghuli\.//' | sort -n | tail -1)
tag="${base}-panghuli.$(( ${last:-0} + 1 ))"
previous=$(git tag -l 'v*-panghuli.*' --sort=-creatordate | head -1)

. .github/panghuli/frontend.env
curl -fsSL -o dist.tar.gz \
  "https://github.com/${FRONTEND_REPO}/releases/download/${FRONTEND_VERSION}/openlist-frontend-dist-${FRONTEND_VERSION}.tar.gz"
echo "${FRONTEND_SHA256}  dist.tar.gz" | sha256sum -c -
find public/dist -mindepth 1 ! -name README.md -exec rm -rf {} +
tar -xzf dist.tar.gz -C public/dist
rm -f dist.tar.gz
test -f public/dist/index.html

{
  echo "tag=${tag}"
  echo "previous=${previous}"
  echo "web_version=${FRONTEND_VERSION}"
  echo "commit=$(git rev-parse --short HEAD)"
  echo "built_at=$(date -u +'%F %T %z')"
} >> "${GITHUB_OUTPUT:-/dev/stdout}"
