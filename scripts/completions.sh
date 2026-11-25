#!/bin/sh
# Writes bash, zsh and fish completions for hlp into ./completions.
# Run by GoReleaser's before hooks; needs no network or config.
set -eu
out=completions
rm -rf "$out"
mkdir -p "$out"
for s in bash zsh fish; do
	go run ./cmd/hlp completion "$s" >"$out/hlp.$s"
done
