#!/bin/sh
# Collects the license (and NOTICE) files of every module linked into hlp,
# for all release OSes, plus Go's own LICENSE, into ./third_party_licenses.
# Run by GoReleaser's before hooks and by `make licenses`.
set -eu
out=third_party_licenses
tools="$(mktemp -d)"
trap 'rm -rf "$tools"' EXIT
# Build the tool for the host; GOOS below only selects which files to analyze.
GOBIN="$tools" go install github.com/google/go-licenses/v2@v2.0.1
rm -rf "$out"
mkdir -p "$out"
for os in darwin linux windows; do
	rm -rf "$tools/l"
	GOOS="$os" "$tools/go-licenses" save ./cmd/hlp --save_path="$tools/l" --force 2>/dev/null
	chmod -R u+w "$tools/l" "$out"
	cp -R "$tools/l/." "$out/"
done
mkdir -p "$out/go"
cp "$(go env GOROOT)/LICENSE" "$out/go/LICENSE" 2>/dev/null ||
	curl -fsSL -o "$out/go/LICENSE" "https://go.dev/LICENSE?m=text"
chmod -R u+w "$out"
