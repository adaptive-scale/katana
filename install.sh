#!/bin/sh
# Install katana.
#
#   curl -fsSL https://raw.githubusercontent.com/adaptive-scale/katana/master/install.sh | sh
#
# Downloads the release binary for this platform. If no release is published for
# the requested version, falls back to building from source with the Go
# toolchain. See --help for options.

set -eu

REPO="adaptive-scale/katana"
MODULE="github.com/adaptive-scale/katana"
BINARY="katana"

VERSION="${KATANA_VERSION:-latest}"
INSTALL_DIR="${KATANA_INSTALL_DIR:-}"
FROM_SOURCE=0
TMP_DIR=""

info() { printf '%s\n' "$*" >&2; }
err() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

cleanup() {
	[ -n "$TMP_DIR" ] && rm -rf "$TMP_DIR"
	return 0
}
trap cleanup EXIT INT TERM

usage() {
	cat >&2 <<EOF
Install $BINARY.

Usage: install.sh [options]

Options:
  --version <tag>   Version to install, e.g. v1.2.3 (default: latest release)
  --dir <path>      Directory to install into (default: /usr/local/bin, or
                    ~/.local/bin when /usr/local/bin is not writable)
  --source          Build from source with the Go toolchain instead of
                    downloading a release binary
  --help            Show this message

Environment:
  KATANA_VERSION      Same as --version
  KATANA_INSTALL_DIR  Same as --dir
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || err "--version needs a value"
		VERSION="$2"
		shift 2
		;;
	--version=*)
		VERSION="${1#--version=}"
		shift
		;;
	--dir)
		[ $# -ge 2 ] || err "--dir needs a value"
		INSTALL_DIR="$2"
		shift 2
		;;
	--dir=*)
		INSTALL_DIR="${1#--dir=}"
		shift
		;;
	--source)
		FROM_SOURCE=1
		shift
		;;
	--help | -h)
		usage
		exit 0
		;;
	*)
		usage
		err "unknown option: $1"
		;;
	esac
done

# --- platform -----------------------------------------------------------------

detect_platform() {
	os=$(uname -s)
	case "$os" in
	Darwin) OS="darwin" ;;
	Linux) OS="linux" ;;
	*)
		err "unsupported operating system: $os (on Windows, use 'go install $MODULE@latest')"
		;;
	esac

	arch=$(uname -m)
	case "$arch" in
	x86_64 | amd64) ARCH="amd64" ;;
	arm64 | aarch64) ARCH="arm64" ;;
	*)
		err "unsupported architecture: $arch"
		;;
	esac
}

# --- download helpers ---------------------------------------------------------

detect_downloader() {
	if command -v curl >/dev/null 2>&1; then
		DOWNLOADER="curl"
	elif command -v wget >/dev/null 2>&1; then
		DOWNLOADER="wget"
	else
		DOWNLOADER=""
	fi
}

# fetch <url> <dest>; returns non-zero if the URL is unavailable.
fetch() {
	case "$DOWNLOADER" in
	curl) curl -fsSL "$1" -o "$2" ;;
	wget) wget -qO "$2" "$1" ;;
	*) return 1 ;;
	esac
}

# latest_tag prints the newest release tag, or nothing if there are no releases.
latest_tag() {
	body="$TMP_DIR/release.json"
	fetch "https://api.github.com/repos/$REPO/releases/latest" "$body" 2>/dev/null || return 0
	sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$body" | head -n 1
}

# --- install location ---------------------------------------------------------

resolve_install_dir() {
	if [ -n "$INSTALL_DIR" ]; then
		mkdir -p "$INSTALL_DIR" 2>/dev/null ||
			err "cannot create install directory: $INSTALL_DIR"
		return
	fi
	if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
		INSTALL_DIR="/usr/local/bin"
	else
		INSTALL_DIR="$HOME/.local/bin"
		mkdir -p "$INSTALL_DIR"
	fi
}

# place <src> moves a built binary into the install directory, elevating only
# when the directory is not writable by the current user.
place() {
	src="$1"
	dest="$INSTALL_DIR/$BINARY"
	chmod +x "$src"
	if [ -w "$INSTALL_DIR" ]; then
		mv -f "$src" "$dest"
	elif command -v sudo >/dev/null 2>&1; then
		info "==> $INSTALL_DIR is not writable, using sudo"
		sudo mv -f "$src" "$dest"
	else
		err "cannot write to $INSTALL_DIR and sudo is unavailable; re-run with --dir <path>"
	fi
	info "==> installed $dest"
}

# --- install strategies -------------------------------------------------------

install_from_release() {
	tag="$1"
	asset="${BINARY}_${tag}_${OS}_${ARCH}"
	url="https://github.com/$REPO/releases/download/$tag/$asset"

	info "==> downloading $BINARY $tag ($OS/$ARCH)"
	# A missing asset is expected (unsupported platform, unreleased version) and
	# handled by the caller, so keep the downloader's own error quiet.
	fetch "$url" "$TMP_DIR/$BINARY" 2>/dev/null || return 1

	# Verify against checksums.txt when the release publishes one.
	if fetch "https://github.com/$REPO/releases/download/$tag/checksums.txt" "$TMP_DIR/checksums.txt" 2>/dev/null; then
		verify_checksum "$TMP_DIR/$BINARY" "$asset" "$TMP_DIR/checksums.txt"
	fi

	place "$TMP_DIR/$BINARY"
}

verify_checksum() {
	file="$1"
	name="$2"
	sums="$3"

	expected=$(sed -n "s/^\([0-9a-f]\{64\}\)[[:space:]]*[*]\{0,1\}$name\$/\1/p" "$sums" | head -n 1)
	[ -n "$expected" ] || return 0

	if command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "$file" | cut -d' ' -f1)
	elif command -v shasum >/dev/null 2>&1; then
		actual=$(shasum -a 256 "$file" | cut -d' ' -f1)
	else
		info "==> no sha256 tool found, skipping checksum verification"
		return 0
	fi

	[ "$actual" = "$expected" ] ||
		err "checksum mismatch for $name (expected $expected, got $actual)"
	info "==> checksum verified"
}

install_from_source() {
	command -v go >/dev/null 2>&1 ||
		err "the Go toolchain is required to build from source; install Go from https://go.dev/dl/"

	ref="$1"
	info "==> building $BINARY $ref from source"
	# GOBIN keeps the build out of the user's Go bin so the binary lands in
	# INSTALL_DIR like the release path does.
	GOBIN="$TMP_DIR/gobin" go install "$MODULE@$ref" ||
		err "go install $MODULE@$ref failed"
	[ -f "$TMP_DIR/gobin/$BINARY" ] || err "build produced no $BINARY binary"

	place "$TMP_DIR/gobin/$BINARY"
}

# --- run ----------------------------------------------------------------------

detect_platform
detect_downloader
resolve_install_dir

TMP_DIR=$(mktemp -d 2>/dev/null || mktemp -d -t katana)

if [ "$FROM_SOURCE" -eq 1 ]; then
	if [ "$VERSION" = "latest" ]; then
		install_from_source "latest"
	else
		install_from_source "$VERSION"
	fi
else
	[ -n "$DOWNLOADER" ] || err "curl or wget is required to download a release; re-run with --source to build instead"

	tag="$VERSION"
	if [ "$tag" = "latest" ]; then
		tag=$(latest_tag)
	fi

	if [ -z "$tag" ]; then
		info "==> no published release found, building from source"
		install_from_source "latest"
	elif ! install_from_release "$tag"; then
		info "==> no release binary for $OS/$ARCH at $tag, building from source"
		install_from_source "$tag"
	fi
fi

# --- verify -------------------------------------------------------------------

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*)
	info ""
	info "$INSTALL_DIR is not on your PATH. Add it with:"
	info "    export PATH=\"\$PATH:$INSTALL_DIR\""
	;;
esac

"$INSTALL_DIR/$BINARY" version >&2 || err "installed binary failed to run"
info ""
info "Get started with: $BINARY init --language go --harness claude"
