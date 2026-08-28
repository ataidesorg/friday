# Installation

Friday ships as one CLI binary. There is no Friday account and no Friday server.
You bring your own model provider credentials, and Friday keeps its state on your
machine.

## Quick Install

Binary installs need a published GitHub Release. Until the first `v*` tag
exists, [build from source](#build-from-source). GitHub's `/releases/latest`
URL ignores pre-releases, so keep the current release as a full release if
you want `install.sh` without `FRIDAY_VERSION` to work.

```console
curl -fsSL https://raw.githubusercontent.com/ataidesorg/friday/main/install.sh | bash
friday version
```

Install a specific tag:

```console
FRIDAY_VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/ataidesorg/friday/main/install.sh | bash
```

Install somewhere explicit:

```console
FRIDAY_INSTALL_DIR=/usr/local/bin curl -fsSL https://raw.githubusercontent.com/ataidesorg/friday/main/install.sh | bash
```

The installer picks this destination order:

1. `$FRIDAY_INSTALL_DIR`
2. `$XDG_BIN_DIR`
3. `$HOME/bin`
4. `$HOME/.friday/bin`

It downloads `friday_<os>_<arch>.tar.gz` from the latest GitHub Release and
requires `sha256sum` or `shasum` to verify `checksums.txt`. Installation
refuses to continue without a matching checksum.

## Build From Source

```console
go install github.com/ataidesorg/friday/cmd/friday@latest
```

`@latest` needs a SemVer tag on the module. Until the first tag is published, clone and `go build` instead.

From a checkout:

```console
git clone https://github.com/ataidesorg/friday.git
cd friday
go build -o bin/friday ./cmd/friday
./bin/friday
```

## Provider Setup

The fastest setup path is the TUI:

```console
friday
/connect
```

`/connect` stores credentials in Friday's encrypted secret store when possible
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
friday providers --check
friday
```

Fireworks is the owner-verified provider path today. Other providers may exist
in the registry, but Friday does not claim live support until that path has been
verified.

## Upgrading

Run the installer again:

```console
curl -fsSL https://raw.githubusercontent.com/ataidesorg/friday/main/install.sh | bash
```

Or install a specific version with `FRIDAY_VERSION`.

## Uninstalling

Remove the binary:

```console
rm -f "$HOME/.friday/bin/friday"
```

If you installed into another directory, remove that copy instead.

Local config and sessions live under the Friday home directory. Remove them only
when you want to delete local state:

```console
rm -rf "$HOME/.friday"
rm -rf "$HOME/.config/friday"
```

## Verify

```console
friday version
friday config validate
```

For a full checkout:

```console
scripts/check.sh --strict
```
