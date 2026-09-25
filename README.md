# iOS Cloud Builder

Build unsigned iOS applications, run their test suites, or deploy signed releases to TestFlight, from Linux, WSL, or Windows using a narrowly scoped remote macOS build. The same privacy model runs a private application's Windows test suite on a Windows runner and returns the Windows build it produces, such as an installer. This is a truthful open-source remote-build/orchestration project derived from [MobAI-App/ios-builder](https://github.com/MobAI-App/ios-builder), not a generic compute service or a disguised workload.

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

The optional TestFlight path adds a second, protected job. The build job encrypts
the unsigned IPA to a transport recipient. After approval of the
`apple-production` Environment, the signing job receives only that authenticated
ciphertext—not the private checkout—then signs and uploads directly to App Store
Connect. GitHub stores no signed IPA artifact.

The original repository backend remains available for existing MobAI users and repository-local builds. Simulator sharing, MobAI integration, Flutter/React Native/KMP development commands, framework detection, working-tree snapshots, signing tools, and public Go wrappers are retained. See [the upstream relationship](docs/UPSTREAM.md).

> [!IMPORTANT]
> GitHub's hosted-runner terms contain a repository-association limitation, and GitHub has not explicitly approved this exact cross-repository architecture. Read [COMPLIANCE.md](COMPLIANCE.md) before using the central hosted-runner backend. The backend boundary is intentionally replaceable by a self-hosted Mac, Codemagic, or another macOS CI implementation.

## Security properties

- Private source is pushed only to the private source repository, under a temporary non-branch ref.
- The GitHub App has Metadata read and Contents read only and is installed on explicitly selected repositories.
- Each job requests an installation token for exactly one source repository, checks out with `persist-credentials: false`, then revokes the token before project code runs.
- Unsigned build remains the default; Apple credentials are available only to the manually protected `apple-production` job.
- The signing job never checks out private source and never runs project scripts or dependencies.
- Detailed dependency/compiler output is redirected to a private log from process start.
- Build logs and locally downloaded IPAs are encrypted to the caller's local-only AGE identity. TestFlight intermediates use a distinct AGE identity held by the protected Environment.
- The public artifact contains only `App.ipa.age` and `build.log.age` (for a test run, `test.log.age` and `report.md.age`, plus `artifact.age` for a passing Windows run that requested one), is retained for one day, and is deleted early when local retrieval succeeds. After a test run whose tests passed and whose outputs were all decrypted, the CLI also deletes the workflow run itself; a run that did not pass is kept for inspection.
- Central mode creates no Actions caches and uploads no DerivedData, dSYM, archive, source, or plaintext diagnostics.
- Inputs are validated before credential creation; build commands use fixed argv arrays, never `eval`. The only script the workflow runs is the test script of `builder ios test` or `builder windows test`: a validated path to a file inside the snapshot, never a command string.
- Full UUIDv4 correlation binds the workflow run and artifact to one build.

These controls protect against accidental public disclosure; they do not sandbox intentionally malicious private project code or hide plaintext from GitHub's active runner. Read the full [threat model](docs/THREAT_MODEL.md).

## Prerequisites

Local workstation (Linux/WSL/macOS; Windows through PowerShell/WSL):

- Git 2.30+
- GitHub CLI (`gh`) authenticated to the source and builder repositories, recommended
- A Git remote using an explicit `github.com` HTTPS or SSH URL
- Builder CLI

Builds use the stable `macos-15` runner image and test runs use `macos-26`; both select Xcode 26.3. Windows test runs use `windows-2025`. No local Mac, Xcode, certificate, or provisioning profile is required for an unsigned central build. TestFlight deployment requires an Apple Distribution certificate, one or more App Store distribution provisioning profiles, and an App Store Connect API key.

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

## Optional protected TestFlight setup

Create a GitHub Environment named `apple-production` in the **public builder**.
Require a reviewer, restrict deployment to the protected default branch, and do
not allow unreviewed workflow changes. For a single-operator repository, leave
**Prevent self-review** disabled so the operator who dispatched the build can
approve it. Environment approval is the point at which Apple credentials become
available to the signing job.

Generate a dedicated AGE identity for transport between the two jobs. Put its
public recipient in the repository variable `APPLE_SIGNING_RECIPIENT`; put the
identity itself only in the Environment secret `APPLE_SIGNING_AGE_IDENTITY`.
Never reuse the caller's local AGE identity and never commit the transport
identity. For example, on a trusted workstation with `age-keygen` installed:

```bash
umask 077
age-keygen -o /tmp/apple-signing.agekey
age-keygen -y /tmp/apple-signing.agekey
```

Copy the printed `age1...` recipient into the repository variable. Copy the
complete `AGE-SECRET-KEY-...` line into the Environment secret, then securely
remove the temporary file.

Configure these values in `apple-production`:

| Kind | Name | Format |
|---|---|---|
| Environment variable | `APPLE_TEAM_ID` | 10-character Apple Team ID |
| Environment secret | `APPLE_SIGNING_AGE_IDENTITY` | dedicated AGE identity |
| Environment secret | `APPLE_DISTRIBUTION_P12` | base64-encoded `.p12` |
| Environment secret | `APPLE_DISTRIBUTION_P12_PASSWORD` | `.p12` password |
| Environment secret | `APPLE_PROVISIONING_PROFILES` | base64-encoded ZIP of App Store `.mobileprovision` files |
| Environment secret (legacy) | `APPLE_PROVISIONING_PROFILE` | base64-encoded single App Store `.mobileprovision`; used when the bundle secret is absent |
| Environment secret | `ASC_API_KEY_P8` | complete `.p8` PEM or its base64 encoding |
| Environment secret | `ASC_KEY_ID` | App Store Connect API key ID |
| Environment secret | `ASC_ISSUER_ID` | App Store Connect issuer UUID for a Team key; omit for an Individual key |

Prefer an Individual App Store Connect key belonging to a dedicated Developer
user whose app access is restricted to the applications this builder may upload.
Individual keys do not use an issuer ID and cannot call Apple's provisioning
endpoints; that is compatible with this manual-profile signing path. If a Team
key is used instead, it applies across all apps and `ASC_ISSUER_ID` is required.
Revoke and replace any certificate, API key, or GitHub App key that was ever
committed, pasted into a public log, or otherwise exposed; moving an exposed
credential into a secret does not make the old credential safe.

Create the multi-application profile bundle on a trusted workstation. Filenames
are only labels: the protected runner selects a profile from its signed
`application-identifier` entitlement and requires an exact Bundle ID match.
Keep only App Store distribution profiles for `APPLE_TEAM_ID` in this ZIP.

```bash
umask 077
zip -j /tmp/apple-provisioning-profiles.zip /trusted/profiles/*.mobileprovision
base64 < /tmp/apple-provisioning-profiles.zip | gh secret set APPLE_PROVISIONING_PROFILES --env apple-production --repo YOUR_ACCOUNT/ios-cloud-builder
```

Securely remove the temporary ZIP after setting the secret. During migration,
both profile secrets may exist; candidates from both are considered. Once at
least one deployment per configured Bundle ID succeeds, the legacy
`APPLE_PROVISIONING_PROFILE` secret can be removed. The bundle is limited by
GitHub's Environment-secret size limit; split certificates/teams across
separate protected builders if the compressed profiles no longer fit.

Verify metadata without reading secret values:

```bash
builder central doctor --testflight
```

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

To create a Release build and upload it to TestFlight:

```bash
builder ios deploy
# equivalent:
builder ios build --testflight
```

The command may wait for approval of `apple-production`. On success, App Store
Connect has accepted the upload for processing; TestFlight processing itself is
asynchronous. No signed IPA is downloaded or retained as a GitHub artifact.
The protected job replaces only `CFBundleVersion` with the unique GitHub Actions
`run_number.run_attempt` value before signing; the application's
`CFBundleShortVersionString` is preserved. It validates the signed IPA with App
Store Connect before uploading it.

## Running tests

`builder ios test` runs the private project's own test suite on the public builder's `macos-26` runner (Xcode 26.3 and its iOS 26.2 simulators), with the same privacy model as a build:

```bash
builder ios test                                   # the script from ios.testScript
builder ios test --script scripts/ios-test.sh --timeout 2h -o dist
```

Set the default script once in `builder.json`; `--script` overrides it:

```json
"ios": { "path": "ios", "testScript": "scripts/ios-test.sh" }
```

The script is a file in the repository, named relative to the repository root with forward slashes, using only letters, digits, `.`, `_`, `+`, `-` and `/`, with no `..`, no `.git`, and no segment starting with `-`. Before snapshotting, the CLI checks that the file exists, stays inside the repository, and is not excluded by `.gitignore`. It then snapshots and dispatches like `ios build` (with `operation: test` and `test_script`), shows progress, downloads and decrypts the results, prints the report, and writes:

```text
./dist/ios-test-<build-id>.log   everything the script printed
./dist/ios-test-<build-id>.md    the script's report.md, when it wrote one
```

The command exits non-zero when the tests failed. The public builder's default branch must contain a workflow with the `test` operation; an older workflow rejects the dispatch.

After the tests pass and both files are decrypted, the command deletes the encrypted artifact and then the whole workflow run from the public builder, so nothing of the run stays there; `--keep-run` keeps both. When anything did not pass (the script exited non-zero or timed out, or the outputs could not be downloaded or decrypted), the run and its encrypted artifact are kept and the run URL is printed; `builder ios logs <build-id>` retrieves the files again while the one-day artifact exists. Deletion needs write access to the builder repository (see [Cleanup and token scopes](#cleanup-and-token-scopes)); if it fails, the command prints a warning with the run URL and still exits with the test result. The private snapshot ref is deleted either way.

### Script contract

The runner executes `bash -- <script>` with the snapshot root as the working directory. `bash` on the macOS image is 3.2.

| Variable | Value |
|---|---|
| `BUILDER_SOURCE_DIR` | Absolute path of the checked-out snapshot, which is also the working directory |
| `BUILDER_IOS_PATH` | `ios.path` from `builder.json`, relative to `BUILDER_SOURCE_DIR` (`.` when unset) |
| `BUILDER_REPORT_DIR` | An empty private directory. Write an optional Markdown summary to `$BUILDER_REPORT_DIR/report.md`; the first 1 MiB is kept |

- Exit status `0` means the tests passed. Any other status, a timeout, or a script that cannot start means they failed.
- The script is stopped (SIGTERM, then SIGKILL after 30 seconds) after 105 minutes; the job's own limit is 120. Anything it leaves running in the background is stopped when it exits.
- Otherwise the script sees the runner's normal environment (`PATH`, `HOME`, `DEVELOPER_DIR`, `CI=true`, Homebrew and the preinstalled toolchains) minus `GITHUB_STEP_SUMMARY`, `GITHUB_OUTPUT`, `GITHUB_ENV`, `GITHUB_PATH`, `GITHUB_STATE`, `GITHUB_TOKEN`, every `ACTIONS_*` and `INPUT_*` variable, and any variable whose name contains `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `PRIVATE_KEY`, `CREDENTIAL` or `AGE_IDENTITY`. The test job has no Apple credentials, no Environment and no signing job.
- The test job does not detect frameworks or install toolchains; the script sets up what it needs (for example CocoaPods, a Flutter SDK, or `npm ci`).
- A log larger than 60 MiB keeps its first 8 MiB and its end.

For example:

```bash
#!/bin/bash
set -uo pipefail
cd "$BUILDER_SOURCE_DIR/$BUILDER_IOS_PATH"
xcodebuild test -quiet -scheme MyApp -destination 'platform=iOS Simulator,name=iPhone 17,OS=26.2'
status=$?
printf '# MyApp tests\n\n`xcodebuild test` exited with status %d.\n' "$status" > "$BUILDER_REPORT_DIR/report.md"
exit "$status"
```

### Test privacy model

- Checkout is identical to a build's: a token scoped to the one source repository, revoked before the script starts.
- The script's stdout and stderr go straight to a private log file. The trusted runner prints only fixed status lines, so nothing the script prints, including workflow commands such as `::error::`, reaches the public log.
- The log and `report.md` are encrypted to your local AGE identity before upload. The artifact `ios-builder-<build-id>` contains only `test.log.age` and `report.md.age`.
- The public run shows the dispatch inputs (including the script path) and only `Tests passed` or `Tests failed. Download the encrypted report using Builder CLI.`
- Environment scrubbing prevents accidental publication, not deliberate exfiltration by the script; see the [threat model](docs/THREAT_MODEL.md).

## Windows builds and tests

`builder windows test` runs a private project's Windows test script on the public builder's `windows-2025` runner, with the same privacy model as an iOS test run, and can return one file the script builds, such as an installer:

```bash
builder windows test                                  # windows.testScript and windows.artifact
builder windows test --script scripts/windows-test.ps1 --artifact dist/Setup.exe --timeout 2h30m -o dist
builder windows test --keep-run                       # keep the public run after a pass
builder windows test --artifact=                      # run the tests without fetching an artifact
```

Set the defaults once in `builder.json`; the flags override them. The section is optional, and configurations without it are unaffected:

```json
"windows": { "testScript": "scripts/windows-test.ps1", "artifact": "dist/Setup.exe" }
```

The command snapshots and dispatches like `ios test` (with `operation: windows-test`, `test_script`, and `artifact_path`), downloads the encrypted results as a stream to disk, decrypts them locally, prints the report, and writes:

```text
./dist/windows-test-<build-id>.log   everything the script printed
./dist/windows-test-<build-id>.md    the script's report.md, when it wrote one
./dist/Setup.exe                     the artifact, under its base name, after a pass
```

It exits non-zero when the tests failed, including when the script passed but the artifact is missing or unusable. If the decrypted artifact would land inside the repository where `.gitignore` does not exclude it, the command warns first: the next snapshot would push it, and GitHub rejects files over 100 MB.

### Windows script contract

- The script is a `.ps1` or `.sh` file in the repository, with the same path rules as the iOS test script. A `.ps1` runs as `pwsh -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File <script>` (PowerShell 7); a `.sh` runs as `bash -- <script>` with Git for Windows' bash. The working directory is the snapshot root, and the runner account is an administrator, so the script may install, run and uninstall what it builds.
- It receives `BUILDER_SOURCE_DIR` (the snapshot root) and `BUILDER_REPORT_DIR` (an empty private directory for an optional `report.md`, of which the first 1 MiB is kept). `BUILDER_IOS_PATH` is not set.
- Exit status `0` means the tests passed; anything else, a timeout, or a script that cannot start means they failed. Use `exit 1` explicitly: a `.ps1` that ends without `exit` reports `0` even after a failed native command unless it checks `$LASTEXITCODE`.
- The artifact is a path relative to the snapshot root, with the same rules. It is read only after the script exited `0`, and only if it resolves, symlinks included, to a regular non-empty file of at most 1 GiB inside the snapshot and outside `.git`. It is streamed through AGE encryption into `artifact.age`; a failing script never produces one. A missing or unusable artifact fails the run and the reason is in the log.
- The script runs in a Windows job object: it starts suspended, joins the job, and only then runs, so every process it starts is in the job. When the script exits, or after 135 minutes (the job's own limit is 150), the whole job is terminated, including MSBuild node-reuse workers, the Roslyn compiler server, and dotnet build servers, and the runner waits until none of them holds the log open.
- The environment and its scrubbing are the iOS test's: the runner's normal environment (`PATH`, Visual Studio and the preinstalled SDKs, `CI=true`, `RUNNER_TEMP`) minus the file-command variables, `GITHUB_TOKEN`, every `ACTIONS_*` and `INPUT_*` variable, and any name containing `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `PRIVATE_KEY`, `CREDENTIAL` or `AGE_IDENTITY`, compared case-insensitively as Windows does.
- The job installs nothing for the project. If the image lacks a toolchain the project needs, such as a newer .NET SDK, the script installs it (for example with `dotnet-install.ps1`).
- Git for Windows checks the snapshot out with CRLF line endings unless `.gitattributes` says otherwise. PowerShell does not mind; a `.sh` script needs `*.sh text eol=lf`.

For example:

```powershell
$ErrorActionPreference = 'Stop'
Set-Location $env:BUILDER_SOURCE_DIR
dotnet test App.sln -c Release --logger "trx;LogFileName=results.trx"
$status = $LASTEXITCODE
if ($status -eq 0) { & ./scripts/build-installer.ps1 -Output dist/Setup.exe; $status = $LASTEXITCODE }
"# Windows tests`n`n``dotnet test`` and the installer build exited with $status." |
  Set-Content -LiteralPath (Join-Path $env:BUILDER_REPORT_DIR 'report.md')
exit $status
```

### Windows privacy model

- Checkout, token revocation, and credential verification are the iOS jobs', in PowerShell: every input reaches the trusted runner as one quoted `--name=value` argument, and PowerShell is used instead of Git Bash so no MSYS path conversion rewrites a value.
- The script's output goes straight to the private log; the public run prints only `Tests passed` or `Tests failed. Download the encrypted report using Builder CLI.`
- The artifact `ios-builder-<build-id>` contains only `test.log.age`, `report.md.age`, and `artifact.age`, encrypted to your local AGE identity, with one-day retention and no compression. The plaintext artifact never leaves the runner's private checkout.
- The job has no Environment, no Apple secrets, and no secret besides the App key used to mint the checkout token.
- The public run shows the dispatch inputs, including the script and artifact paths. After a pass the CLI deletes the run; see below.

### Cleanup and token scopes

A test run leaves nothing behind in the public builder when its tests pass: after downloading and decrypting every output, `builder ios test` and `builder windows test` delete the encrypted artifact (`DELETE /repos/{owner}/{repo}/actions/artifacts/{id}`) and then the workflow run with its logs (`DELETE /repos/{owner}/{repo}/actions/runs/{run_id}`), retrying for up to about 30 seconds while GitHub still finishes the run. `--keep-run` skips this. A run whose tests did not pass, whose artifact was missing, or whose outputs could not be downloaded or decrypted is kept, with its encrypted artifact for one day, so it can be inspected and retrieved again; the command prints its URL. The temporary snapshot ref in the private repository is always deleted.

Deleting runs and artifacts needs write access to the builder repository. The CLI uses the first token it finds in `BUILDER_GITHUB_TOKEN`, `GH_TOKEN`, `GITHUB_TOKEN`, the GitHub CLI (`gh auth token`), or the token from `builder auth github`; `builder central doctor` shows which. Deletion needs the `repo` scope, which `builder auth github` (`repo workflow`) and a default GitHub CLI login both have. If deletion fails, the command prints a warning with the run URL and still exits with the test result. To fix it:

- GitHub CLI token without `repo`: `gh auth refresh -h github.com -s repo,workflow`
- Builder's own token: `builder auth github` again
- Fine-grained token: grant it **Actions: Read and write** on the builder repository (dispatching needs it too)
- Or delete the run by hand from its page (the ... menu, **Delete workflow run**)

## Supported projects

- Native Swift/Objective-C iOS projects and workspaces
- Flutter
- React Native and ejected Expo
- Kotlin Multiplatform iOS applications
- Cordova/Ionic generated iOS projects
- XcodeGen manifests

Schemes and workspaces/projects are detected where possible. The source build always passes `CODE_SIGNING_ALLOWED=NO` and packages the device `.app` as an unsigned IPA. In TestFlight mode, the separate protected job manually signs that app with the matching App Store provisioning profile from the protected multi-application bundle. App extensions in the app's `PlugIns` directory (widgets, Live Activities, share and notification extensions) are signed too, each with its own App Store profile, discovered or created through App Store Connect exactly like the app's, before the app itself; an extension's Bundle ID must extend the app's, and its build number is set to the app's. Watch apps, App Clips, XPC services, ExtensionKit extensions, and bundles nested inside an extension still require provisioning this job does not perform and are rejected rather than partially signed.

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

`builder ios share` and `builder signing setup` remain repository-backend features. Central TestFlight credentials are configured only in the public builder's protected Environment; the CLI never downloads them.

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

With `--testflight`, doctor additionally verifies metadata for
`APPLE_SIGNING_RECIPIENT`, the `apple-production` Environment, `APPLE_TEAM_ID`,
every required Environment secret, either `APPLE_PROVISIONING_PROFILES` or the
legacy `APPLE_PROVISIONING_PROFILE`, a non-empty required-reviewer rule with
self-review allowed, and an exact custom branch allowlist containing only the
builder's default branch. It cannot verify secret values or profile coverage.

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
- A malicious project, dependency, or test script runs as the runner user and is not strongly sandboxed. On Windows that user is an administrator, and a deliberately hostile script can start processes outside its job object (through a service, a scheduled task, or WMI).
- The central hosted-runner design has the policy caveat described in [COMPLIANCE.md](COMPLIANCE.md).
- Central TestFlight supports multiple top-level applications by exact Bundle ID and signs their `PlugIns` app extensions, but still rejects Watch apps, App Clips, XPC services, ExtensionKit extensions, and bundles nested inside an extension.
- A successful upload means App Store Connect accepted the binary; it does not mean Apple's asynchronous processing or review has completed.
- Private GitHub SSH aliases are rejected in central mode because the CLI cannot prove an alias resolves to GitHub; use an explicit `git@github.com:OWNER/REPO.git` or `https://github.com/OWNER/REPO.git` remote.
- Failed artifact deletion is non-fatal; ciphertext expires after one day. A test run that did not pass is kept on purpose, and a run whose deletion failed stays until it is deleted by hand or expires with the repository's log retention.
- `builder ios logs <build-id>` retrieves a Windows run's log and report, not its artifact; rerun `builder windows test` for a fresh artifact.

## License

MIT. The original license and Git history are retained. See [LICENSE](LICENSE) and [docs/UPSTREAM.md](docs/UPSTREAM.md).
