# Releasing hlp

Releases are GitHub Releases built by [GoReleaser](https://goreleaser.com) from
`.github/workflows/release.yml` when a `v*` tag is pushed. The workflow needs no secrets beyond the
built-in `GITHUB_TOKEN`.

Each release has:

- `hlp_<version>_<os>_<arch>.tar.gz` for macOS and Linux (amd64, arm64) and a `.zip` for Windows,
  each with the licence, README, shell completions and the licences of every bundled dependency;
- `checksums.txt` (SHA-256), which `install.sh` verifies;
- signed build provenance: `gh attestation verify <archive> -R maxmoneycash/hlp`.

## Cutting a release

1. `make check` is green on `main`, and CI is green.
2. Move the `Unreleased` notes in `CHANGELOG.md` under the new version and commit.
3. Optional dry run: `goreleaser release --snapshot --clean` builds everything into `./dist`.
4. Tag and push: `git tag -a v0.2.0 -m "hlp v0.2.0" && git push origin v0.2.0`. Tags with a
   suffix (`v0.2.0-rc1`) are published as prereleases.
5. Check: `gh release view v0.2.0 --repo maxmoneycash/hlp` lists the archives and `checksums.txt`,
   and `curl -fsSL https://raw.githubusercontent.com/maxmoneycash/hlp/main/install.sh | sh` then
   `hlp version` prints the new version.
