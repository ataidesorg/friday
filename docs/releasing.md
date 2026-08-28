# Releasing Friday

This is the release checklist for publishing Friday from GitHub.

## Principles

- Release only from a clean, reviewed commit on `main`.
- Move the `Unreleased` section of [CHANGELOG.md](../CHANGELOG.md) under the new
  version heading before tagging.
- Never publish secrets, local config, or session files.
- Do not tag a release that has not passed `scripts/check.sh --strict`.
- Release notes should name real features and known gaps. Do not imply provider,
  sandbox, OAuth, or marketplace support that has not been verified.

## Local Preflight

```console
git status --short
scripts/check.sh --strict
GORELEASER_CURRENT_TAG=v0.1.0 goreleaser release --snapshot --clean
```

Inspect `dist/`:

```console
ls -lh dist
(cd dist && shasum -a 256 -c checksums.txt)
```

Expected archives:

- `friday_vX.Y.Z_darwin_amd64.tar.gz`
- `friday_vX.Y.Z_darwin_arm64.tar.gz`
- `friday_vX.Y.Z_linux_amd64.tar.gz`
- `friday_vX.Y.Z_linux_arm64.tar.gz`
- Stable latest aliases named `friday_<os>_<arch>.tar.gz`
- `checksums.txt`

`dist/` also holds GoReleaser's own `artifacts.json`, `metadata.json`,
`config.yaml`, and the unarchived binaries. Only the archives and
`checksums.txt` are published.

## Tag Release

```console
git tag v0.1.0
git push origin v0.1.0
```

The `release` workflow builds all archives, uploads them as workflow artifacts,
and creates a GitHub Release for the tag with generated notes. Do not mark
that GitHub Release as a pre-release if `install.sh` without `FRIDAY_VERSION`
should work: GitHub's `/releases/latest` ignores pre-releases.

## Manual Workflow Build

Use the `release` workflow's manual dispatch to test packaging without creating
a GitHub Release. The workflow requires a version string like `v0.1.0`, runs
GoReleaser in snapshot mode so nothing is published, and uploads the same
archives as workflow artifacts.

## Post-Release Verification

After the GitHub Release is live:

```console
FRIDAY_VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/ataidesorg/friday/main/install.sh | bash
friday version
```

Check the release page:

- All four versioned archives are present.
- All four stable alias archives are present.
- `checksums.txt` is present.
- Generated notes describe the real diff.
- The README install command works on a clean machine.

Verify build provenance, which proves an archive was produced by this
repository's `release` workflow rather than uploaded by hand:

```console
gh attestation verify friday_v0.1.0_darwin_arm64.tar.gz --repo ataidesorg/friday
```

`checksums.txt` only proves the download was not corrupted in transit: it
ships from the same release as the binaries, so anyone who can publish a
release can publish matching sums. The attestation is the authenticity check.

## Rollback

If a release is bad:

1. Mark the GitHub Release as a pre-release or delete it.
2. Delete the tag locally and remotely only after deciding that the version must
   disappear rather than be superseded.
3. Prefer a patch release, for example `v0.1.1`, when users may already have the
   bad tag.

## Future Package Managers

The release archives are designed to feed package managers without changing the
core release process:

- Homebrew formula
- Scoop manifest
- Nix package
- Arch AUR package

Add those only after the first binary release is stable enough to keep updated.
