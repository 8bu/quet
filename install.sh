#!/bin/sh
# Installs quet — https://github.com/8bu/quet
#
#   curl -fsSL https://raw.githubusercontent.com/8bu/quet/main/install.sh | sh
#
# Downloads the release binary for this OS and CPU, checks it against the
# release's SHA256SUMS, and installs it as `quet`. Re-running upgrades.
#
# Environment:
#   QUET_VERSION       version to install, e.g. 0.1.1 (default: the latest release)
#   QUET_INSTALL_DIR   install directory (default: /usr/local/bin when writable,
#                      otherwise ~/.local/bin)
#   QUET_RELEASES_URL  releases base URL, for mirrors
#                      (default: https://github.com/8bu/quet/releases)
#
# Everything runs inside main, called on the last line, so a download cut off
# halfway through never executes a partial script.
set -eu

say() { printf 'quet-install: %s\n' "$*"; }
die() {
	printf 'quet-install: error: %s\n' "$*" >&2
	exit 1
}

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		die "curl or wget is required"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{ print $1 }'
	else
		die "sha256sum or shasum is required to verify the download"
	fi
}

main() {
	case $(uname -s) in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) die "unsupported OS $(uname -s): quet ships for macOS and Linux" ;;
	esac
	case $(uname -m) in
	arm64 | aarch64) arch=arm64 ;;
	x86_64 | amd64) arch=amd64 ;;
	*) die "unsupported CPU $(uname -m): quet ships for arm64 and amd64" ;;
	esac
	asset=quet_${os}_${arch}

	releases=${QUET_RELEASES_URL:-https://github.com/8bu/quet/releases}
	if [ -n "${QUET_VERSION:-}" ]; then
		base=$releases/download/v${QUET_VERSION#v}
	else
		base=$releases/latest/download
	fi

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	say "downloading $asset from $base"
	fetch "$base/$asset" "$tmp/$asset" || die "could not download $base/$asset"
	fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || die "could not download $base/SHA256SUMS"

	want=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/SHA256SUMS")
	[ -n "$want" ] || die "SHA256SUMS has no entry for $asset"
	got=$(sha256 "$tmp/$asset")
	[ "$got" = "$want" ] || die "checksum mismatch for $asset (expected $want, got $got); nothing was installed"
	chmod +x "$tmp/$asset"

	dir=${QUET_INSTALL_DIR:-}
	if [ -z "$dir" ]; then
		if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
			dir=/usr/local/bin
		else
			dir=$HOME/.local/bin
		fi
	fi
	mkdir -p "$dir" || die "could not create $dir"
	mv -f "$tmp/$asset" "$dir/quet" || die "could not write $dir/quet"

	say "installed $("$dir/quet" --version) to $dir/quet"
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) say "$dir is not on your PATH; add it with: export PATH=\"$dir:\$PATH\"" ;;
	esac
}

main "$@"
