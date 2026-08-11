# jvm-switcher

[![CI](https://github.com/ralscha/jvm-switcher/actions/workflows/ci.yml/badge.svg)](https://github.com/ralscha/jvm-switcher/actions/workflows/ci.yml)
[![Release](https://github.com/ralscha/jvm-switcher/actions/workflows/release.yml/badge.svg)](https://github.com/ralscha/jvm-switcher/actions/workflows/release.yml)

A small, dependency-free CLI for installing and switching between JDKs. By default it downloads Eclipse Temurin from the [Adoptium API](https://api.adoptium.net/). A JSON catalog can provide Corretto, Zulu, Microsoft OpenJDK, Oracle JDK, GraalVM, or other distributions.

## Install

Download the archive for your operating system and architecture from [GitHub Releases](https://github.com/ralscha/jvm-switcher/releases). Release assets use these names:

```text
jvm-switcher_VERSION_linux_amd64.tar.gz
jvm-switcher_VERSION_linux_arm64.tar.gz
jvm-switcher_VERSION_darwin_amd64.tar.gz
jvm-switcher_VERSION_darwin_arm64.tar.gz
jvm-switcher_VERSION_windows_amd64.zip
jvm-switcher_VERSION_windows_arm64.zip
```

Extract the archive, put `jvm-switcher` or `jvm-switcher.exe` on your `PATH`, and verify the installation:

```text
jvm-switcher version
```

Each release includes `checksums.txt`. On Linux, verify downloaded release files with:

```sh
sha256sum --ignore-missing -c checksums.txt
```

On macOS, use the built-in `shasum` and select the downloaded archive's line:

```sh
grep 'jvm-switcher_VERSION_darwin_ARCH.tar.gz$' checksums.txt | shasum -a 256 -c -
```

GitHub CLI can verify the build-provenance attestation:

```sh
gh attestation verify jvm-switcher_VERSION_OS_ARCH.EXT --repo ralscha/jvm-switcher
```

### Build from source

Go 1.26.5 or newer is required.

```powershell
go build -o jvm-switcher.exe .
```

On Linux or macOS:

```sh
go build -o jvm-switcher .
```

The repository also includes a [Taskfile](Taskfile.yml). With [Task](https://taskfile.dev/) installed:

```sh
task build
task test
task vet
task verify
```

`task verify` runs tests, vet, a build, and golangci-lint. The lint task uses Docker. Release helpers require GoReleaser:

```sh
task release-check
task snapshot
```

`task snapshot` writes local release archives to `dist/` without publishing them.

## Shell setup

The active JDK is always exposed at `~/.jvm-switcher/current`. Configure `JAVA_HOME` and `PATH` once, then open a new terminal.

PowerShell on Windows:

```powershell
$root = Join-Path $HOME ".jvm-switcher"
$javaHome = Join-Path $root "current"
$javaBin = Join-Path $javaHome "bin"
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
[Environment]::SetEnvironmentVariable("JAVA_HOME", $javaHome, "User")
if (($userPath -split ";") -notcontains $javaBin) {
    [Environment]::SetEnvironmentVariable("Path", "$javaBin;$userPath", "User")
}
```

Bash or Zsh on Linux and macOS:

```sh
export JAVA_HOME="${JVM_SWITCHER_HOME:-$HOME/.jvm-switcher}/current"
export PATH="$JAVA_HOME/bin:$PATH"
```

Add those two lines to the appropriate shell profile, such as `~/.bashrc` or `~/.zshrc`.

## Usage

```text
jvm-switcher show
jvm-switcher install 21
jvm-switcher install corretto@21
jvm-switcher exec 21 -- java -version
jvm-switcher exec -- ./gradlew test
jvm-switcher list
jvm-switcher current
jvm-switcher doctor
jvm-switcher outdated
jvm-switcher update --all
jvm-switcher prune --dry-run
jvm-switcher switch corretto@21.0.12.8.1
jvm-switcher switch 1
jvm-switcher remove 2
jvm-switcher version
```

Commands and aliases:

```text
list, ls       List current JDK installations.
install, i     Install an available remote JDK.
exec           Run a command with a selected JDK.
switch, s      Switch to a specified version or index number.
remove, rm     Remove a specific version or index number.
current        Show the active JDK.
which          Show a command in the active JDK.
doctor         Check the jvm-switcher setup.
outdated       Show installed JDKs with newer patch releases.
update         Install newer patch releases.
prune          Remove superseded inactive patch releases.
show           Show versions available for download.
version        Show the jvm-switcher version.
help, h        Show all commands or help for one command.
```

With the default source, `show` lists one latest GA Temurin JDK per available Java feature release and names the source it read. `install` accepts its displayed index, a feature version such as `21`, a full version, `distribution@feature`, or `distribution@version`. The first JDK installed becomes active automatically; later ones need an explicit `switch`. `list` displays the indices accepted by `switch` and `remove`.

If multiple distributions match a feature or version, use a `distribution@version` identifier or the displayed index. This prevents similarly-versioned distributions from being confused.

The active JDK cannot be removed. Switch to another installed version first.

## Project JDKs

Put one selector in `.jvm-switcher` or `.java-version` at a project root:

```text
temurin@21
```

Commands search from the working directory toward the filesystem root. When both files are present in one directory, `.jvm-switcher` takes precedence. A selector without a full patch version chooses the newest matching installed or available release.

```text
jvm-switcher install
jvm-switcher exec -- java -version
jvm-switcher exec -- ./gradlew test
jvm-switcher update
```

`exec` sets `JAVA_HOME` and prepends the selected JDK's `bin` directory to `PATH` only for the child process. It does not change the global `current` link, so concurrent projects can use different JDKs.

## Inspection and updates

`current` reports the active JDK, `which` resolves a command below the managed `current` path, and `doctor` checks the link, shell variables, executable, and `java -version`. Use `current --quiet` in shell scripts.

`outdated` compares installed JDKs with the configured source. `update <selector>` or `update --all` installs newer patches and keeps an updated active JDK active. Older patches are retained for rollback until `prune`; inspect removals first with `prune --dry-run`.

`list`, `show`, `current`, and `outdated` support JSON output. `show` can also be filtered without changing its displayed install indices:

```text
jvm-switcher list --json
jvm-switcher current --json
jvm-switcher show --distribution temurin --feature 21
jvm-switcher show --lts --json
jvm-switcher show --installed
jvm-switcher outdated --json
```

## Storage

JDKs are stored below `~/.jvm-switcher/jdks`. On Windows, `current` is a directory junction; on Linux and macOS, it is a symbolic link. The JDK that `current` resolves to is the active one, so nothing else has to be kept in sync.

Mutating operations use an operating-system file lock. Concurrent installs may download in parallel, but installation, switching, removal, update activation, and pruning are serialized.

Set `JVM_SWITCHER_HOME` to use another root directory. If it is set, update the one-time shell configuration above to point `JAVA_HOME` at that root's `current` directory.

## Hosted catalog

A custom catalog replaces the default Adoptium list. Copy [catalog.json](catalog.json), update its releases, and commit it to a GitHub repository. Then create `~/.jvm-switcher/config.json` containing the raw GitHub URL:

```json
{
    "catalog": "https://raw.githubusercontent.com/YOUR_ORG/YOUR_REPO/main/catalog.json"
}
```

Use a `raw.githubusercontent.com` URL, not a GitHub `/blob/` page URL. For a temporary override, set `JVM_SWITCHER_CATALOG` to an HTTP(S) URL or local file path:

```powershell
$env:JVM_SWITCHER_CATALOG = "https://raw.githubusercontent.com/YOUR_ORG/YOUR_REPO/main/catalog.json"
jvm-switcher show
```

The catalog uses [catalog.schema.json](catalog.schema.json). Each release entry has:

| Field | Required | Meaning |
| --- | --- | --- |
| `distribution` | yes | Short identifier such as `temurin`, `corretto`, `zulu`, or `microsoft` |
| `feature` | yes | Java feature version, such as `17` or `21` |
| `version` | yes | Full vendor release version |
| `os` | yes | Go OS name: `windows`, `linux`, or `darwin` |
| `arch` | yes | Go architecture name, commonly `amd64` or `arm64` |
| `url` | yes | Absolute HTTP(S) archive URL, or a URL relative to a hosted catalog |
| `sha256` | yes | The archive's 64-character SHA-256 checksum |
| `size` | no | Archive size in bytes, used only for display |
| `file_name` | no | Archive filename when the URL path does not end in `.zip`, `.tar.gz`, or `.tgz` |

Only entries matching the current OS and architecture are shown. ZIP, TAR.GZ, and TGZ JDK archives are supported. The archive must contain exactly one JDK home with `bin/java` or `bin/java.exe`. Downloads always have their SHA-256 checksum verified before extraction, are capped at the declared `size`, and archive entries may neither escape the extraction directory nor exceed a fixed expansion budget.

## Releases

Pushing a valid semantic-version tag such as `v1.0.0` runs the protected release workflow. Release tags must resolve to commits on `main`; tags such as `v1.0.0-rc.1` are published as prereleases automatically.

GoReleaser publishes Windows, Linux, and macOS archives for `amd64` and `arm64`, embeds the release version in the executable, generates `checksums.txt`, and creates GitHub build-provenance attestations for both the archives and checksum manifest.

Before the first release:

1. Create the GitHub repository as `ralscha/jvm-switcher` and push `main`.
2. Protect `main` and require the CI workflow to pass before merging.
3. Create a GitHub Actions environment named `release`; optionally require a reviewer for deployments to it.
4. Keep the default `GITHUB_TOKEN` permissions restricted. The release job requests its write and attestation permissions explicitly.

To publish a release, start from a clean, tested commit on `main`, create an annotated tag, and push it:

```sh
task release-tag TAG=v1.0.0
```

That task runs the local verification gates, validates the GoReleaser configuration, creates an annotated tag, and pushes it. The equivalent manual sequence is:

```sh
git switch main
git pull --ff-only
go test ./...
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0
```

The release workflow revalidates the remote tag immediately before publishing, so moving or replacing a verified tag causes the job to fail.

## License

[MIT](LICENSE)