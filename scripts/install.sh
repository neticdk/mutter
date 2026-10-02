#!/usr/bin/env bash
# Install the latest mutter release, or the tag given as $1. Needs gh logged
# in with access to neticdk/mutter.
set -euo pipefail

repo=neticdk/mutter
bindir=${BINDIR:-$HOME/.local/bin}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*)
	echo "unsupported architecture: $(uname -m)" >&2
	exit 1
	;;
esac
archive="mutter_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

gh release download ${1:+"$1"} -R "$repo" -D "$tmp" -p "$archive" -p checksums.txt
(cd "$tmp" && grep " $archive\$" checksums.txt | shasum -a 256 -c -)
tar -xzf "$tmp/$archive" -C "$tmp" mutter
mkdir -p "$bindir"
install -m 0755 "$tmp/mutter" "$bindir/mutter"

echo "installed $("$bindir/mutter" --version) to $bindir/mutter"
case ":$PATH:" in
*":$bindir:"*) ;;
*) echo "add $bindir to your PATH" ;;
esac
