# Installing `text`

Pick the path that fits your platform. All install methods deliver the same statically-linked `text` binary.

## macOS / Linux — one-liner

```sh
curl -fsSL https://raw.githubusercontent.com/KLIXPERT-io/text-cli/refs/heads/main/install.sh | sh
```

The installer detects your OS (`linux` / `darwin`) and architecture (`amd64` / `arm64`), downloads the latest release archive plus `checksums.txt`, verifies the SHA-256, and installs `text` to `/usr/local/bin` (if writable) or `~/.local/bin`.

Pin a version or override the install location:

```sh
TEXT_VERSION=v1.2.3 INSTALL_DIR="$HOME/bin" \
  curl -fsSL https://raw.githubusercontent.com/KLIXPERT-io/text-cli/refs/heads/main/install.sh | sh
```

Run `sh install.sh --help` for the full list of options.

## Windows — one-liner (PowerShell 5.1+)

```powershell
irm https://raw.githubusercontent.com/KLIXPERT-io/text-cli/refs/heads/main/install.ps1 | iex
```

Installs `text.exe` to `%LOCALAPPDATA%\Programs\text\` (no admin required). Override with environment variables:

```powershell
$env:TEXT_VERSION = 'v1.2.3'
$env:INSTALL_DIR = "$env:USERPROFILE\bin"
irm https://raw.githubusercontent.com/KLIXPERT-io/text-cli/refs/heads/main/install.ps1 | iex
```

> The first run may show a SmartScreen warning because the binary is not (yet) Authenticode-signed. Choose "Run anyway" — or verify the SHA-256 manually (see below).

## Manual download

Grab the archive for your platform from the [Releases page](https://github.com/KLIXPERT-io/text-cli/releases/latest):

| Platform        | Archive                                 |
| --------------- | --------------------------------------- |
| Linux amd64     | `text_<version>_linux_amd64.tar.gz`     |
| Linux arm64     | `text_<version>_linux_arm64.tar.gz`     |
| macOS amd64     | `text_<version>_darwin_amd64.tar.gz`    |
| macOS arm64     | `text_<version>_darwin_arm64.tar.gz`    |
| Windows amd64   | `text_<version>_windows_amd64.zip`      |

`<version>` carries no leading `v` — the tag `v1.2.3` produces `text_1.2.3_linux_amd64.tar.gz`.

Extract, then move `text` (or `text.exe`) into a directory on your `$PATH`.

## With a Go toolchain

```sh
go install github.com/KLIXPERT-io/text-cli/cmd/text@latest
```

The binary lands in `$(go env GOBIN)` (or `$(go env GOPATH)/bin`). Note: this build does not embed the release tag in `text --version`.

## From source

```sh
git clone https://github.com/KLIXPERT-io/text-cli.git
cd text-cli
make build      # produces ./text
make install    # installs via `go install`
```

## Verifying checksums manually

Every release ships a `checksums.txt` file alongside the archives. To verify before installing:

```sh
curl -fsSLO https://github.com/KLIXPERT-io/text-cli/releases/download/v1.2.3/text_1.2.3_linux_amd64.tar.gz
curl -fsSLO https://github.com/KLIXPERT-io/text-cli/releases/download/v1.2.3/checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing
```

## Pinning a version

```sh
TEXT_VERSION=v1.2.3 curl -fsSL https://raw.githubusercontent.com/KLIXPERT-io/text-cli/refs/heads/main/install.sh | sh
```

```powershell
$env:TEXT_VERSION = 'v1.2.3'
irm https://raw.githubusercontent.com/KLIXPERT-io/text-cli/refs/heads/main/install.ps1 | iex
```

## Auto-Update

Once installed, `text` keeps itself current. On every invocation a background goroutine checks the GitHub Releases API at most once per 24 hours; if a newer stable tag is published and the running binary is writable, it downloads the matching archive, verifies the SHA-256 against `checksums.txt`, and atomically swaps the binary in place. The current command is unaffected — the next `text` invocation runs the new version.

### Disabling auto-update

Two equivalent opt-outs:

```bash
export TEXT_NO_UPDATE=1
```

Or in the config file (`text config path`):

```toml
auto_update = false
```

Equivalently: `text config set auto_update false`.

When either is set, no network requests are made and `update-state.json` is not touched. `TEXT_NO_UPDATE=0` and `TEXT_NO_UPDATE=false` are treated as "not set".

### Managed installs are skipped automatically

`text` detects package-managed binaries by install-path prefix and never auto-updates them — updates come through the package manager instead. The detected prefixes are:

- `/opt/homebrew`, `/usr/local/Cellar` (Homebrew)
- `/home/linuxbrew` (Linuxbrew)
- `/snap` (Snap)
- `/var/lib/flatpak` (Flatpak)
- `C:\ProgramData\chocolatey` (Chocolatey)
- `C:\Users\*\scoop` (Scoop)
- `C:\Program Files`

A binary that is not writable by the current user (or owned by a different uid on unix) is also skipped.

### Inspecting / forcing updates

```bash
text update status   # current + latest version, last check time, enabled state (with reason if disabled)
text update check    # force a check now, bypassing the 24h throttle
text update apply    # force download + atomic swap to the latest version
```

`update status` also prints the resolved install path and the last-installed version recorded in state. `update check` and `update apply` still respect the opt-out and managed-install guards.

### Post-update notice

After a successful background update, the next `text` command prints a one-line notice to stderr before its normal output:

```
text: updated to vX.Y.Z (was vA.B.C)
```

Suppress it with `TEXT_NO_UPDATE_NOTICE=1`.

## Cutting a release (maintainers)

The release version lives in the [`VERSION`](./VERSION) file at the repo root. To ship a new release:

1. Bump `VERSION` (e.g. `0.1.0` → `0.2.0`) and merge to `main`.
2. The `Auto Tag & Release` workflow reads the file, creates a matching `vX.Y.Z` git tag, and triggers the release pipeline (`release.yml`).
3. GoReleaser builds the five archives and publishes a GitHub Release with `checksums.txt`.

Manual fallback: `git tag v0.2.0 && git push --tags` runs the same release pipeline directly.

Every push to `main` and every pull request also runs `ci.yml`: build, vet, `gofmt` check, and the unit tests.

## Uninstalling

Delete the binary:

```sh
rm "$(command -v text)"
```

```powershell
Remove-Item "$env:LOCALAPPDATA\Programs\text\text.exe"
```

To remove the configuration and cache as well:

```sh
rm -rf "$(dirname "$(text config path)")"   # run before deleting the binary
```
