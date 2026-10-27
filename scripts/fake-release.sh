#!/bin/sh
# Lays out the binaries the way GitHub serves a release, for testing the
# installer. Serve the root folder on 127.0.0.1 with any web server.
set -eu
if [ "$#" -ne 3 ]; then
    echo 'Usage: scripts/fake-release.sh <dist> <root> <tag>' >&2
    exit 1
fi
dist=$1
root=$2
tag=$3
case "$tag" in v[0-9]*.[0-9]*.[0-9]*) ;;
    *) echo 'Expected a release tag such as v0.1.0' >&2; exit 1 ;;
esac
case "$tag" in */*) echo 'Release tag must not contain slashes' >&2; exit 1 ;; esac
# Check all files first, so a missing one leaves nothing half-written.
assets='mailmd-linux-amd64 mailmd-linux-arm64 mailmd-darwin-amd64 mailmd-darwin-arm64 mailmd-windows-amd64.exe mailmd-windows-arm64.exe checksums.txt'
for asset in $assets; do
    [ -f "$dist/$asset" ] || { echo "Missing release file: $asset" >&2; exit 1; }
done
for layout in "$root/releases/latest/download" "$root/releases/download/$tag"; do
    mkdir -p "$layout"
    for asset in $assets; do cp "$dist/$asset" "$layout/$asset"; done
done
(cd "$root" && pwd -P)
