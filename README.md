# iOS Cloud Builder

Build unsigned iOS applications from Linux, WSL, or Windows using a narrowly scoped remote macOS build. This is a truthful open-source remote-build/orchestration project derived from [MobAI-App/ios-builder](https://github.com/MobAI-App/ios-builder), not a generic compute service or a disguised workload.

The central backend lets multiple private application repositories use one public builder repository without committing private source or publishing plaintext output:

```text
private app working tree
  -> temporary private refs/ios-builder/jobs/<uuid>
  -> public builder workflow on macOS
  -> repository-scoped GitHub App checkout
  -> unsigned iOS build with private console redirection
  -> AGE-encrypted IPA + log artifact
  -> local download, decryption, IPA validation, and cleanup
```

The original repository backend remains available for existing MobAI users and repository-local builds. Simulator sharing, MobAI integration, Flutter/React Native/KMP development commands, framework detection, working-tree snapshots, signing tools, and public Go wrappers are retained. See [the upstream relationship](docs/UPSTREAM.md).

> [!IMPORTANT]
> GitHub's hosted-runner terms contain a repository-association limitation, and GitHub has not explicitly approved this exact cross-repository architecture. Read [COMPLIANCE.md](COMPLIANCE.md) before using the central hosted-runner backend. The backend boundary is intentionally replaceable by a self-hosted Mac, Codemagic, or another macOS CI implementation.

## Security properties

- Private source is pushed only to the private source repository, under a temporary non-branch ref.
- The GitHub App has Metadata read and Contents read only and is installed on explicitly selected repositories.
- Each job requests an installation token for exactly one source repository, checks out with `persist-credentials: false`, then revokes the token before project code runs.
- Central v1 is unsigned-only and accepts no signing material or Apple credentials.
- Detailed dependency/compiler output is redirected to a private log from process start.
- IPA and build log are encrypted with a local-only AGE X25519 identity before upload.
- The public artifact contains only `App.ipa.age` and `build.log.age`, is retained for one day, and is deleted early when local retrieval succeeds.
- Central mode creates no Actions caches and uploads no DerivedData, dSYM, archive, source, or plaintext diagnostics.
- Inputs are validated before credential creation; build commands use fixed argv arrays, never `eval` or user-provided scripts.
- Full UUIDv4 correlation binds the workflow run and artifact to one build.

These controls protect against accidental public disclosure; they do not sandbox intentionally malicious private project code or hide plaintext from GitHub's active runner. Read the full [threat model](docs/THREAT_MODEL.md).

## Prerequisites

Local workstation (Linux/WSL/macOS; Windows through PowerShell/WSL):

- Git 2.30+
- GitHub CLI (`gh`) authenticated to the source and builder repositories, recommended
- A Git remote using an explicit `github.com` HTTPS or SSH URL
- Builder CLI

The public builder workflow uses the stable `macos-15` runner image. No local Mac, Xcode, certificate, or provisioning profile is required for an unsigned central build.

## Install the CLI

From a published release:

```bash
curl -fsSL https://raw.githubusercontent.com/ori2015/ios-cloud-builder/main/install.sh | bash
```

Or build from source with Go 1.24 or newer:

```bash
git clone https://github.com/ori2015/ios-cloud-builder.git
cd ios-cloud-builder
go build -o builder ./cmd/builder
install -m 0755 builder ~/.local/bin/builder
```

Windows users can download `builder-windows-amd64.exe` from Releases and place it on `PATH`.

Authentication resolution order is `BUILDER_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN`, an authenticated `gh`, then the legacy Builder credential store. Tokens are never written to `builder.json` or logged. Usually this is enough:

```bash
gh auth login
gh auth status
```

The legacy device flow remains available for repository-mode compatibility:

```bash
builder auth github
```

## One-time public builder setup

This repository itself is the public builder. Fork it publicly without rewriting history, retain `.github/workflows/ios-build.yml`, and restrict write access to trusted operators.

Create a dedicated GitHub App in **Settings -> Developer settings -> GitHub Apps -> New GitHub App** with these exact settings:

| Setting | Value |
|---|---|
| GitHub App name | `ios-cloud-builder-<your-account>` (globally unique) |
| Homepage URL | URL of your public builder repository |
| Webhook | Inactive |
| Repository permissions: Contents | Read-only |
| Repository permissions: Metadata | Read-only (implicit) |
| Every other repository permission | No access |
| Organization permissions | No access |
| Where can this GitHub App be installed? | Only on this account |

Generate one private key. In the **public builder repository**, configure:

```text
Repository variable  APP_CLIENT_ID   = GitHub App client ID
Repository secret    APP_PRIVATE_KEY = complete generated PEM private key
```

Install the App using **Only select repositories** and choose only private applications this builder may read. Do not grant Actions, Issues, Pull requests, Administration, or write permissions. Delete the downloaded PEM after the repository secret is verified, or retain a recovery copy only in an appropriate secrets manager. Never commit it.

Protect the builder's default branch, require review for CODEOWNERS paths, keep administrators minimal, and leave the default workflow token permission at read-only.

## Add a private application

From each authorized private application:

```bash
cd /path/to/private-app
builder central setup --builder YOUR_ACCOUNT/ios-cloud-builder
builder central doctor
```

`central setup` detects the source GitHub repository, project type, and common iOS path; creates/reuses a local AGE identity; and writes a configuration like:

```json
{
  "project": "MyApp",
  "platform": "ios",
  "backend": "central",
  "github": { "owner": "SOURCE_OWNER", "repo": "PRIVATE_SOURCE_REPO" },
  "builder": {
    "owner": "YOUR_ACCOUNT",
    "repo": "ios-cloud-builder",
    "workflow": "ios-build.yml"
  },
  "security": { "recipient": "age1..." },
  "ios": { "path": "ios", "scheme": "", "configuration": "Debug" }
}
```

Only the public AGE recipient is stored in this file. The private identity is kept in the OS keyring when usable or under the user's configuration directory with `0600` permissions. To initialize it explicitly:

```bash
builder security init
```

Existing upstream `builder.json` files migrate automatically to `backend: repository`. Adding valid `builder` and `security` fields without a backend migrates to central. `builder init --backend central --builder OWNER/REPO` is the interactive alternative and deliberately does not create `.github/workflows` in the private repository.

## Daily use

```bash
cd /path/to/any/authorized/private-ios-project
builder ios build
```

The command snapshots staged, unstaged, and untracked non-ignored files without modifying the branch, real index, or working tree. It pushes only the temporary private ref, dispatches the public builder, shows high-level progress, downloads ciphertext, decrypts locally, validates the IPA ZIP and app structure, and writes:

```text
./dist/MyApp.ipa
```

The private ref and encrypted public artifact are deleted best-effort. A failure automatically downloads and decrypts diagnostics under `dist/`. If retrieval was interrupted, retry while the one-day artifact still exists:

```bash
builder ios logs <build-uuid>
```

Remove abandoned refs older than the default 24 hours:

```bash
builder cleanup
builder cleanup --older-than 48h
```

Snapshot creation respects `.gitignore`. An untracked secret that is not ignored will be included in the temporary private snapshot, so review ignore rules before building.

## Supported projects

- Native Swift/Objective-C iOS projects and workspaces
- Flutter
- React Native and ejected Expo
- Kotlin Multiplatform iOS applications
- Cordova/Ionic generated iOS projects
- XcodeGen manifests

Schemes and workspaces/projects are detected where possible. Central builds always pass `CODE_SIGNING_ALLOWED=NO` and package the device `.app` as an unsigned IPA. Central v1 does not upload certificates or provisioning profiles.

## Repository backend and retained commands

Set `"backend": "repository"` to retain the original behavior where workflows live in the application repository. This mode continues to support repository-local signing and simulator sharing.

```bash
builder init
builder ios build
builder ios share
builder signing csr
builder signing p12
builder signing setup
builder dev flutter
builder dev rn
builder dev kmp
builder mobai ping
builder update
```

`builder ios share` and `builder signing setup` are intentionally repository-backend features. Central v1 installs no private-repository workflow and stores no Apple signing secrets.

## Doctor checks

`builder central doctor` verifies, without printing credentials:

- Git and local GitHub authentication source
- config schema and source/builder separation
- public builder and private source API access
- central workflow availability
- `APP_CLIENT_ID` variable and `APP_PRIVATE_KEY` secret metadata
- matching local AGE identity
- explicit GitHub source remote
- dry-run permission to push the temporary snapshot namespace

## Updating and contributing

See [docs/UPSTREAM.md](docs/UPSTREAM.md) for the preserved upstream baseline and merge procedure. Before submitting:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/builder
go build ./cmd/builder-runner
```

## Known limitations

- A one-time GitHub App browser setup and private-repository selection cannot be completed safely by the CLI alone.
- Repository/source names and workflow inputs are public metadata even though source contents and outputs are encrypted.
- A malicious project or dependency runs as the runner user and is not strongly sandboxed.
- The central hosted-runner design has the policy caveat described in [COMPLIANCE.md](COMPLIANCE.md).
- Central v1 produces unsigned IPAs only.
- Private GitHub SSH aliases are rejected in central mode because the CLI cannot prove an alias resolves to GitHub; use an explicit `git@github.com:OWNER/REPO.git` or `https://github.com/OWNER/REPO.git` remote.
- Failed artifact deletion is non-fatal; ciphertext expires after one day.

## License

MIT. The original license and Git history are retained. See [LICENSE](LICENSE) and [docs/UPSTREAM.md](docs/UPSTREAM.md).
