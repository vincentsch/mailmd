#!/bin/sh
# Usage: curl -fsSL https://raw.githubusercontent.com/vincentsch/mailmd/main/install.sh | sh
set -eu

fail() { printf '%s\n' "mailmd: $*" >&2; exit 1; }
work=
staged=
cleanup() {
    [ -z "$staged" ] || rm -f "$staged"
    [ -z "$work" ] || rm -rf "$work"
    return 0
}
trap cleanup 0
trap 'exit 1' HUP INT TERM

os=$(uname -s)
case "$os" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    MINGW*|MSYS*|CYGWIN*) os=windows ;;
    *) fail "Unsupported operating system: $os. mailmd has binaries for Linux, macOS and Windows." ;;
esac
arch=$(uname -m)
case "$arch" in
    x86_64|amd64|AMD64) arch=amd64 ;;
    aarch64|arm64|ARM64) arch=arm64 ;;
    *) fail "Unsupported architecture: $arch. mailmd has binaries for amd64 and arm64." ;;
esac
asset=mailmd-$os-$arch
binary=mailmd
if [ "$os" = windows ]; then asset=$asset.exe; binary=$binary.exe; fi
base=${MAILMD_DOWNLOAD_BASE:-https://github.com/vincentsch/mailmd}
if [ -n "${MAILMD_VERSION:-}" ]; then
    # Release tags start with v, like v0.1.0.
    case "$MAILMD_VERSION" in v*) tag=$MAILMD_VERSION ;; *) tag=v$MAILMD_VERSION ;; esac
    url=${base%/}/releases/download/$tag
else
    url=${base%/}/releases/latest/download
fi
command -v curl >/dev/null 2>&1 || fail 'The installer needs curl to download mailmd.'
work=$(mktemp -d) || fail 'Cannot create a temporary folder for the download.'
curl -fsSL "$url/$asset" -o "$work/$asset" || fail "Cannot download $asset from $url."
curl -fsSL "$url/checksums.txt" -o "$work/checksums.txt" || fail "Cannot download checksums.txt from $url."
expected=$(awk -v name="$asset" '$2 == name {print $1}' "$work/checksums.txt")
[ -n "$expected" ] || fail "checksums.txt has no entry for $asset."
[ "${#expected}" -eq 64 ] || fail "checksums.txt has a broken entry for $asset."
case "$expected" in *[!0-9a-fA-F]*) fail "checksums.txt has a broken entry for $asset." ;; esac
if command -v sha256sum >/dev/null 2>&1; then
    digest=$(sha256sum "$work/$asset") || fail 'Cannot compute the SHA-256 checksum of the download.'
elif command -v shasum >/dev/null 2>&1; then
    digest=$(shasum -a 256 "$work/$asset") || fail 'Cannot compute the SHA-256 checksum of the download.'
else
    fail 'Need sha256sum or shasum to check the download.'
fi
actual=${digest%% *}
[ "$actual" = "$expected" ] || fail "The download of $asset doesn't match its checksum, so nothing was installed."

install_dir=${INSTALL_DIR:-$HOME/.local/bin}
mkdir -p "$install_dir" || fail "Cannot create $install_dir. Choose another folder with INSTALL_DIR."
# The new binary goes into the same folder first, so the last step is a
# rename on one file system and never leaves a half-copied mailmd.
staged=$(mktemp "$install_dir/.mailmd.XXXXXX") || fail "Cannot write to $install_dir. Choose another folder with INSTALL_DIR."
cp "$work/$asset" "$staged" || fail "Cannot copy the binary into $install_dir."
chmod 755 "$staged" || fail 'Cannot make the binary executable.'
mv -f "$staged" "$install_dir/$binary" || fail "Cannot replace $install_dir/$binary. If mailmd is running, stop it and try again."
staged=
printf 'Installed %s.\n' "$install_dir/$binary"
case ":${PATH:-}:" in
    *":$install_dir:"*) ;;
    *) printf '%s is not on your PATH. Add it with:\n  export PATH="%s:$PATH"\n' "$install_dir" "$install_dir" ;;
esac
printf 'Next: mailmd demo\n'
