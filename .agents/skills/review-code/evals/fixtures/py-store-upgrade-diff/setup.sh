#!/usr/bin/env bash
# Build this fixture's git repository in an empty directory: base/ committed on
# main, then change/ committed on feat/total-cents, which is left checked out.
#
# Usage: setup.sh <empty-dir>
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
dest="${1:?usage: setup.sh <empty-dir>}"

mkdir -p "$dest"
cp -R "$here/base/." "$dest/"
git -C "$dest" init -q -b main
git -C "$dest" config user.name fixture
git -C "$dest" config user.email fixture@example.com
git -C "$dest" add -A
git -C "$dest" commit -q -m "orders store"
git -C "$dest" checkout -q -b feat/total-cents
cp -R "$here/change/." "$dest/"
git -C "$dest" add -A
git -C "$dest" commit -q -m "store order totals in cents"
