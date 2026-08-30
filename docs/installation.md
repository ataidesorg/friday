# Installation

Ink ships as one CLI binary. There is no Ink account and no Ink server.
You bring your own model provider credentials, and Ink keeps its state on your
machine.

## Quick Install

Binary installs need a published GitHub Release. Until the first `v*` tag
exists, [build from source](#build-from-source). GitHub's `/releases/latest`
URL ignores pre-releases, so keep the current release as a full release if
you want `install.sh` without `INK_VERSION` to work.

```console
curl -fsSL https://raw.githubusercontent.com/ataidesorg/ink/main/install.sh | bash
ink version
```

Install a specific tag:

```console
INK_VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/ataidesorg/ink/main/install.sh | bash
```

Install somewhere explicit:

```console
INK_INSTALL_DIR=/usr/local/bin curl -fsSL https://raw.githubusercontent.com/ataidesorg/ink/main/install.sh | bash
```

The installer picks this destination order:

1. `$INK_INSTALL_DIR`
2. `$XDG_BIN_DIR`
3. `$HOME/bin`
4. `$HOME/.ink/bin`

It downloads `ink_<os>_<arch>.tar.gz` from the latest GitHub Release and
requires `sha256sum` or `shasum` to verify `checksums.txt`. Installation
refuses to continue without a matching checksum.

## Build From Source

```console
go install github.com/ataidesorg/ink/cmd/ink@latest
```

`@latest` needs a SemVer tag on the module. Until the first tag is published, clone and `go build` instead.

From a checkout:

```console
git clone https://github.com/ataidesorg/ink.git
cd ink
go build -o bin/ink ./cmd/ink
./bin/ink
```

## Provider Setup

The fastest setup path is the TUI:

```console
ink
/connect
```

`/connect` stores credentials in Ink's encrypted secret store when possible
and writes the route into user config. It does not put keys in project files,
logs, prompts, or command arguments.

You can also use environment-backed config:

```toml
[providers.fireworks]
kind = "openai_compatible"
base_url = "https://api.fireworks.ai/inference/v1"
privacy = "public_cloud"

[providers.fireworks.auth]
source = "env"
name = "FIREWORKS_API_KEY"

[models.routes.fast]
provider = "fireworks"
model = "accounts/fireworks/models/deepseek-v4-flash-0731"

[models.routing]
default = "fast"
```

Then:

```console
export FIREWORKS_API_KEY="..."
ink providers --check
ink
```

Fireworks is the owner-verified provider path today. Other providers may exist
in the registry, but Ink does not claim live support until that path has been
verified.

## Upgrading

Run the installer again:

```console
curl -fsSL https://raw.githubusercontent.com/ataidesorg/ink/main/install.sh | bash
```

Or install a specific version with `INK_VERSION`.

## Uninstalling

Remove the binary:

```console
rm -f "$HOME/.ink/bin/ink"
```

If you installed into another directory, remove that copy instead.

Local config and sessions live under the Ink home directory. Remove them only
when you want to delete local state:

```console
rm -rf "$HOME/.ink"
rm -rf "$HOME/.config/ink"
```

## Verify

```console
ink version
ink config validate
```

For a full checkout:

```console
scripts/check.sh --strict
```
