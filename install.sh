#!/bin/sh
# hlp installer: downloads a release binary, verifies its SHA-256 against the
# release's checksums.txt, and installs it.
#
#   curl -fsSL https://raw.githubusercontent.com/maxmoneycash/hlp/main/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --version v0.1.0 --dir /usr/local/bin
#
# Options (flags win over env):
#   --version, -v   release tag, e.g. v0.1.0        (HLP_VERSION, default: latest)
#   --dir, -d       install directory               (HLP_INSTALL_DIR)
#   --help, -h
# Default directory: ~/.local/bin (created if missing). The script never calls
# sudo, and never installs into Homebrew's prefix unless --dir says so.
#
# HLP_INSTALL_VERIFY=1 also checks the archive's signed build provenance with
# `gh attestation verify` (needs the GitHub CLI) and refuses on failure.
#
# Advanced: HLP_INSTALL_BASE_URL overrides the download base (a mirror); the
# script appends /<tag>/<file>. Default https://github.com/maxmoneycash/hlp/releases/download
set -eu

REPO="maxmoneycash/hlp"
BIN="hlp"
BASE_URL="${HLP_INSTALL_BASE_URL:-https://github.com/${REPO}/releases/download}"
VERSION="${HLP_VERSION:-latest}"
INSTALL_DIR="${HLP_INSTALL_DIR:-}"

say() { printf '%s\n' "$*" >&2; }
die() {
	say "hlp install: $*"
	exit 1
}

usage() {
	cat >&2 <<'EOF_USAGE'
Usage: install.sh [--version vX.Y.Z] [--dir DIR]

  --version, -v   release tag (HLP_VERSION, default: latest)
  --dir, -d       install directory (HLP_INSTALL_DIR, default: ~/.local/bin)

Downloads hlp from GitHub releases, verifies its SHA-256 against
checksums.txt, and installs it. Never uses sudo. Set HLP_INSTALL_VERIFY=1
to also check the signed build provenance with gh.
EOF_USAGE
}

while [ $# -gt 0 ]; do
	case "$1" in
	-v | --version)
		[ $# -ge 2 ] || die "$1 needs a value"
		VERSION="$2"
		shift 2
		;;
	--version=*)
		VERSION="${1#*=}"
		shift
		;;
	-d | --dir)
		[ $# -ge 2 ] || die "$1 needs a value"
		INSTALL_DIR="$2"
		shift 2
		;;
	--dir=*)
		INSTALL_DIR="${1#*=}"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown option: $1 (try --help)" ;;
	esac
done

has() { command -v "$1" >/dev/null 2>&1; }

# fetch URL DEST: download to a file, failing on HTTP errors.
fetch() {
	if has curl; then
		curl -fsSL --proto '=https,file' --tlsv1.2 -o "$2" "$1"
	elif has wget; then
		# BusyBox wget lacks --https-only; the SHA-256 check still applies.
		wget -q --https-only -O "$2" "$1" 2>/dev/null || wget -q -O "$2" "$1"
	else
		die "need curl or wget"
	fi
}

detect_os() {
	case "$(uname -s)" in
	Darwin) echo darwin ;;
	Linux) echo linux ;;
	MINGW* | MSYS* | CYGWIN* | Windows_NT)
		die "on Windows, download the .zip from https://github.com/${REPO}/releases or run: go install github.com/${REPO}/cmd/hlp@latest"
		;;
	*) die "unsupported OS: $(uname -s)" ;;
	esac
}

detect_arch() {
	arch="$(uname -m)"
	# An x86_64 shell under Rosetta on Apple silicon should still get arm64.
	if [ "$arch" = "x86_64" ] && [ "$(uname -s)" = "Darwin" ] &&
		[ "$(sysctl -in sysctl.proc_translated 2>/dev/null || echo 0)" = "1" ]; then
		arch=arm64
	fi
	case "$arch" in
	x86_64 | amd64) echo amd64 ;;
	arm64 | aarch64) echo arm64 ;;
	*) die "unsupported architecture: $arch" ;;
	esac
}

# latest_tag prints the newest release tag from the /releases/latest redirect
# (no API token, no rate limit).
latest_tag() {
	url=""
	if has curl; then
		url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")" || true
	elif has wget; then
		url="$(wget -q -S --spider "https://github.com/${REPO}/releases/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r')" || true
	fi
	tag="${url##*/}"
	case "$tag" in
	v[0-9]*) echo "$tag" ;;
	*) die "could not resolve the latest release; pass --version vX.Y.Z" ;;
	esac
}

sha256_of() {
	if has sha256sum; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif has shasum; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	elif has openssl; then
		openssl dgst -sha256 "$1" | sed 's/^.*= *//'
	else
		die "need sha256sum, shasum or openssl to verify the download"
	fi
}

# pick_dir prefers ~/.local/bin even when /usr/local/bin is writable: on Intel
# Macs that is Homebrew's prefix.
pick_dir() {
	if [ -n "$INSTALL_DIR" ]; then
		echo "$INSTALL_DIR"
	else
		echo "${HOME}/.local/bin"
	fi
}

main() {
	os="$(detect_os)"
	arch="$(detect_arch)"
	tag="$VERSION"
	if [ "$tag" = "latest" ]; then
		tag="$(latest_tag)"
	fi
	case "$tag" in
	v*) ;;
	*) tag="v${tag}" ;;
	esac
	version="${tag#v}"
	archive="${BIN}_${version}_${os}_${arch}.tar.gz"

	tmp="$(mktemp -d 2>/dev/null || mktemp -d -t hlp)"
	trap 'rm -rf "$tmp"' EXIT
	# POSIX sh resumes after an INT/TERM handler; exit so EXIT cleans up.
	trap 'exit 130' INT TERM

	say "hlp install: downloading ${tag} for ${os}/${arch}"
	fetch "${BASE_URL}/${tag}/${archive}" "${tmp}/${archive}" || die "download failed: ${BASE_URL}/${tag}/${archive}"
	fetch "${BASE_URL}/${tag}/checksums.txt" "${tmp}/checksums.txt" || die "download failed: checksums.txt"

	want="$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1 }' "${tmp}/checksums.txt")"
	[ -n "$want" ] || die "${archive} is not listed in checksums.txt"
	got="$(sha256_of "${tmp}/${archive}")"
	if [ "$want" != "$got" ]; then
		die "checksum mismatch for ${archive} (want ${want}, got ${got}); not installing"
	fi
	say "hlp install: sha256 verified"
	# checksums.txt proves integrity, not authenticity (same origin). Releases
	# carry signed build provenance; HLP_INSTALL_VERIFY=1 checks it with gh.
	if [ "${HLP_INSTALL_VERIFY:-}" = "1" ]; then
		has gh || die "HLP_INSTALL_VERIFY=1 needs the GitHub CLI (gh)"
		gh attestation verify "${tmp}/${archive}" -R "$REPO" >/dev/null ||
			die "provenance check failed for ${archive}; not installing"
		say "hlp install: build provenance verified"
	fi

	tar -xzf "${tmp}/${archive}" -C "$tmp" "$BIN" || die "archive does not contain ${BIN}"

	dir="$(pick_dir)"
	mkdir -p "$dir" || die "cannot create ${dir}"
	[ -w "$dir" ] || die "${dir} is not writable; pass --dir or run with a directory you own"
	# Write next to the target, then rename: never a half-written binary.
	cp "${tmp}/${BIN}" "${dir}/.${BIN}.new"
	chmod 0755 "${dir}/.${BIN}.new"
	mv -f "${dir}/.${BIN}.new" "${dir}/${BIN}"
	# Unsigned binaries (decision 12): clear the macOS quarantine flag if a
	# browser or other tool set one, and say so.
	if [ "$os" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
		if xattr -p com.apple.quarantine "${dir}/${BIN}" >/dev/null 2>&1; then
			xattr -d com.apple.quarantine "${dir}/${BIN}" 2>/dev/null &&
				say "hlp install: removed the macOS quarantine flag from ${dir}/${BIN} (the binary is not notarized yet)"
		fi
	fi

	say "hlp install: installed ${dir}/${BIN} (${tag})"
	case ":${PATH}:" in
	*":${dir}:"*) ;;
	*) say "hlp install: ${dir} is not on your PATH; add: export PATH=\"${dir}:\$PATH\"" ;;
	esac
	say "Try: hlp chart BTC 15m"
}

main
