# Git strategy

Trunk-based development. `main` is the only long-lived branch and is always
releasable: CI green, `scripts/check.sh --strict` passing, no known-broken
state merged.

## Branches

- **`main`** — the trunk. Every release is tagged from it.
- **Short-lived work branches**, one per change, deleted after merge. Name them
  with the same vocabulary as commit types, plus a short kebab-case
  description: `feat/provider-wizard`, `fix/prompt-queue-empty`,
  `docs/git-strategy`, `refactor/session-store`, `test/`, `chore/`, `perf/`,
  `ci/`.

There is no `develop` and no `release/*`. Git-flow exists to maintain several
release trains at once; Friday ships one version at a time from one branch, and
a second long-lived branch would only add merge debt.

## Change flow

1. Branch from an up-to-date `main`.
2. Write or update tests first when behavior changes.
3. Commit using [Conventional Commits](../CONTRIBUTING.md#commit-style).
4. Push and open a pull request. `ci.yml` runs on every push and every PR.
5. Merge only with CI green and the strict gate passing.
6. **Squash merge.** Delete the branch.

Squash keeps `main` at one commit per change, which is what `git log --oneline`
and the changelog are actually read for. Work-in-progress commits stay in the
pull request, where they are still available for review.

To update a branch, rebase it onto `main`. Do not merge `main` into a work
branch — it makes the pull request diff unreadable. Never rebase, force-push,
or delete `main`.

## Releases

Tags drive releases. Nothing else does.

1. Confirm `main` is green.
2. Move the `## [Unreleased]` section of [CHANGELOG.md](../CHANGELOG.md) under
   the new version heading.
3. `git tag v0.1.0 && git push origin v0.1.0`.
4. [`.github/workflows/release.yml`](../.github/workflows/release.yml) fires on
   `v*`, runs the strict gate, then builds and publishes.

Versioning is [SemVer](https://semver.org/). Before v1.0.0 a minor bump may
break compatibility; after it, only a major may.

**Published tags are immutable.** Never move or re-point one. `install.sh`
resolves assets by tag and the Go module proxy caches by tag, so a moved tag
serves two different binaries under one name. A bad release is superseded by a
patch release, never by a retag.

Full checklist: [docs/releasing.md](releasing.md).

## Hotfixes

Same flow, no special branch type: branch from `main`, fix, PR, squash, tag a
patch release. Before v1.0.0 there is no support window for older minors, so
there is nothing to back-port to.

## Branch protection

Settings the repository owner applies to `main` on GitHub:

- Require a pull request before merging.
- Require the `ci` status check to pass.
- Require branches to be up to date before merging.
- Allow squash merging only; disable merge commits and rebase merging.
- Automatically delete head branches after merge.
- Block force pushes and branch deletion.

## What runs when

| Event | Workflow | What it does |
| --- | --- | --- |
| Push to any branch | `ci.yml` | Strict gate, full-history secret scan, fixture project's own tests |
| Pull request | `ci.yml` | The same checks |
| Push a `v*` tag | `release.yml` | Strict gate, then build and publish the GitHub Release |
| Manual dispatch | `release.yml` | Builds and uploads workflow artifacts without creating a Release |
