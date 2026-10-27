#!/bin/sh
# Installs the agentlog binary from the latest GitHub release. No Go needed.
#
#   curl -fsSL https://raw.githubusercontent.com/Ryheff24/agentlog/main/install.sh | sh
#
# AGENTLOG_VERSION       a tag such as v0.1.0 (default: the latest release)
# AGENTLOG_INSTALL_DIR   where the binary goes (default: ~/.local/bin)
#
# Before anything is installed, the archive is checked against the release's
# checksums.txt, and on macOS the binary must carry a valid Developer ID
# signature from the team below. That signature is made on the maintainer's
# Mac, not on GitHub, so it holds even if the repository is compromised.
#
# Everything runs from main at the very end, so a download cut off halfway
# runs nothing.
set -eu

repo=Ryheff24/agentlog
# The Apple Team ID the macOS binary must be signed by. An unsigned or
# differently signed binary is refused.
team_id="M2DUW2UUKH"

fail() {
	echo "agentlog install: $*" >&2
	exit 1
}

main() {
	version=${AGENTLOG_VERSION:-latest}
	dir=${AGENTLOG_INSTALL_DIR:-$HOME/.local/bin}
	protos="=https"
	if [ -n "${AGENTLOG_BASE_URL:-}" ]; then
		base=$AGENTLOG_BASE_URL # for testing against a local copy
		protos="=https,file"
	elif [ "$version" = latest ]; then
		base=https://github.com/$repo/releases/latest/download
	else
		base=https://github.com/$repo/releases/download/$version
	fi

	os=$(uname -s)
	case "$os" in
	Darwin) asset=agentlog-darwin-universal ;;
	Linux)
		case "$(uname -m)" in
		x86_64 | amd64) asset=agentlog-linux-amd64 ;;
		aarch64 | arm64) asset=agentlog-linux-arm64 ;;
		*) fail "no prebuilt binary for Linux $(uname -m); build from source (see the README)" ;;
		esac
		;;
	*) fail "no prebuilt binary for $os; build from source (see the README)" ;;
	esac

	if command -v curl > /dev/null 2>&1; then
		fetch() { curl -fsSL --proto "$protos" --tlsv1.2 -o "$2" "$1"; }
	elif command -v wget > /dev/null 2>&1; then
		[ "$protos" = "=https" ] || fail "testing against AGENTLOG_BASE_URL needs curl"
		fetch() { wget -q --https-only -O "$2" "$1"; }
	else
		fail "needs curl or wget"
	fi
	if command -v sha256sum > /dev/null 2>&1; then
		sha256() { sha256sum "$1" | cut -d' ' -f1; }
	elif command -v shasum > /dev/null 2>&1; then
		sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
	else
		fail "needs sha256sum or shasum to check the download"
	fi

	tmp=$(mktemp -d "${TMPDIR:-/tmp}/agentlog-install.XXXXXX")
	trap 'rm -rf "$tmp"' EXIT

	fetch "$base/$asset.tar.gz" "$tmp/$asset.tar.gz" || fail "could not download $base/$asset.tar.gz"
	fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "could not download $base/checksums.txt"
	want=$(awk -v f="$asset.tar.gz" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || fail "$asset.tar.gz is not listed in checksums.txt"
	[ "$(sha256 "$tmp/$asset.tar.gz")" = "$want" ] || fail "checksum mismatch for $asset.tar.gz; nothing installed"

	mkdir "$tmp/x"
	tar -xzf "$tmp/$asset.tar.gz" -C "$tmp/x" agentlog
	bin=$tmp/x/agentlog
	[ -f "$bin" ] && [ ! -L "$bin" ] || fail "the archive does not hold a plain agentlog binary"

	if [ "$os" = Darwin ] && [ -n "$team_id" ]; then
		codesign --verify --strict "$bin" 2> /dev/null || fail "the macOS binary is not validly signed; nothing installed"
		got=$(codesign -dv "$bin" 2>&1 | sed -n 's/^TeamIdentifier=//p')
		[ "$got" = "$team_id" ] || fail "the macOS binary is signed by team '$got', not $team_id; nothing installed"
	fi

	mkdir -p "$dir"
	chmod 0755 "$bin"
	mv -f "$bin" "$dir/agentlog"
	echo "installed $("$dir/agentlog" --version) to $dir/agentlog"

	case ":$PATH:" in
	*":$dir:"*) ;;
	*) echo "$dir is not on your PATH; add it, e.g. to ~/.zshrc or ~/.bashrc:
  export PATH=\"$dir:\$PATH\"" ;;
	esac
}

main "$@"
