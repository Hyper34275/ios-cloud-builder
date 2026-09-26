# Threat model

## Security goals

Central mode is designed so a public repository never commits private application source, uploads a plaintext private IPA, detailed build or test log, test report, or Windows test artifact, or keeps a persistent private-repository credential in code, and so a passing test run leaves no workflow run behind in the public repository. The local CLI pushes the working tree only to a temporary ref in the private source repository. A repository-scoped GitHub App installation token performs the private checkout. Only AGE ciphertext is uploaded. Optional Apple signing material and a dedicated transport AGE identity exist only as secrets of the protected `apple-production` Environment.

## Trust boundaries

- The local workstation and its AGE identity are trusted.
- The private source repository and its write-authorized operator are trusted to initiate builds.
- GitHub's runner and Actions control plane necessarily see plaintext while the job runs.
- Public Actions logs, artifacts, caches, inputs, summaries, annotations, and public pull requests are untrusted/public territory.
- Maintainers with write/admin access to the public builder are highly trusted. They can modify the workflow or helper and can dispatch builds. Keep this group minimal.
- Private application build scripts, dependencies, and the test script of a test run execute arbitrary code on the runner. On the Windows runner that code runs as an administrator. Environment scrubbing prevents accidental access to known Actions channels and tokens, but is not a sandbox and cannot prevent all network exfiltration by a malicious project.
- The TestFlight signing job trusts the approved public-builder revision, GitHub Environment controls, GitHub Actions control plane, Apple signing material, and the authenticated unsigned IPA produced by the first job. It never checks out or executes private project source.

## Threats and mitigations

### Malicious public pull request

The private-source workflow is `workflow_dispatch` only. PR CI has read-only permissions and no central secrets. External actions are pinned to full commit SHAs. CODEOWNERS identifies security-sensitive paths. Default-branch protection and required review must also be enabled in repository settings.

### Workflow modification or hostile maintainer

An authorized writer could change the trusted helper, select a private repository installed for the App, or substitute their own AGE recipient. Protect `main`, require review for `.github/workflows/ios-build.yml`, `internal/runner`, `internal/security`, and central coordination code, and audit dispatches. Repository administrators remain trusted because they can bypass repository controls.

For TestFlight, also require approval on `apple-production` and restrict it to the protected default branch. Approval must be based on the exact workflow revision being run. Environment protection cannot defend against a repository administrator who can change or bypass those rules.

### Stolen GitHub App private key

The App has only Metadata read and Contents read, and is installed using **Only select repositories**. Every workflow requests a token for exactly one validated repository. Rotate the App key and review installations immediately after suspected compromise. A stolen key can read every selected repository until revoked; scoping a single job does not change the App installation's overall blast radius.

### Token persistence and project-code access

Both checkouts use `persist-credentials: false`. The App token is passed only to the private checkout and is explicitly revoked before dependency, build, or test code runs. The build helper removes GitHub/Actions token and file-command variables from child environments. A test script inherits the runner's environment minus the job summary and file-command variables (`GITHUB_STEP_SUMMARY`, `GITHUB_OUTPUT`, `GITHUB_ENV`, `GITHUB_PATH`, `GITHUB_STATE`), `GITHUB_TOKEN`, every `ACTIONS_*` runtime, results, cache, and OIDC variable, `INPUT_*`, and any name that looks like a credential. Names are compared case-insensitively, as Windows treats them; a Windows runner exposes the same Actions channels as a macOS one, so the same list applies to both. The test jobs reference no secret other than the App key used to mint the token. The workflow must never print environment variables, remotes, tokens, or secret values.

### Input, expression, path, and shell injection

The trusted Go helper validates UUID, owner, repository, exact snapshot ref, relative iOS path, scheme, configuration, framework enum, X25519 recipient, and test script path before token minting. Private paths are resolved after checkout and must remain inside the source root. Build processes receive fixed argv arrays; there is no `eval`, arbitrary command, or generic shell input.

The one script input, `test_script`, is accepted only with `operation: test` or `operation: windows-test` and rejected otherwise; for `windows-test` it must end in `.ps1` or `.sh`. Before credentials exist it must be a clean relative path of portable characters with no `..`, `.git`, control characters, backslashes, segment starting with `-`, or segment ending in `.` (Windows drops a trailing dot, so `.git.` would name `.git`). After checkout it must resolve, symlinks included, to a regular file inside the snapshot and outside `.git`. It reaches the runner only through an environment variable and runs as `bash -- <path>`, or for a `.ps1` as `pwsh -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File <path>`; it is never interpolated into a shell command, so it names a file from the authorized snapshot and cannot carry a command.

`artifact_path` is accepted only with `operation: windows-test`, with the same path rules, and is never executed: after the script exits 0 the runner resolves it again, symlinks included, and reads it only as a regular file inside the snapshot and outside `.git`. The Windows job's steps run in PowerShell rather than Git Bash, so MSYS never rewrites an argument or environment value that looks like a POSIX path, and each input reaches the trusted runner as a single quoted `--name=value` argument, so an empty value cannot shift the arguments after it.

### Artifact, log, and cache disclosure

Compiler/dependency output starts redirected into a private build log. Both IPA and log are AGE-encrypted before upload, plaintext files are deleted, and the artifact step uses an exact ciphertext allowlist with one-day retention. The CLI binds the artifact to the exact run/build UUID, decrypts locally, validates IPA structure, and attempts remote artifact deletion. Central mode uses no Actions cache and never uploads DerivedData, dSYMs, archives, source, or plaintext diagnostics.

A test run applies the same rules to a different pair of files. The script's stdout and stderr are the private log file itself from process start, so no output passes through the trusted helper, which prints only fixed status lines; workflow commands the script prints are never interpreted. The script runs in its own process group, which is stopped when it exits or after the 135-minute timeout, so a background process cannot keep writing once the outputs are encrypted. Its report is read only from a fresh private directory, only as a regular file, and only up to 1 MiB. The log and report are encrypted to the caller, their plaintext and the report directory are deleted, and the upload allowlists exactly `test.log.age` and `report.md.age`. The public log says only whether the tests passed.

A Windows test run adds one file. On Windows the script is created suspended, assigned to a job object that forbids breakaway and is killed when its handle closes, and only then resumed, so every process it starts is in the job; the job is terminated when the script exits or after the 135-minute timeout, and the runner waits until its processes are gone, so no lingering compiler server or MSBuild node can hold the log open or keep writing. Only when the script exited 0 is `artifact_path` read, as a regular file of at most 1 GiB that did not change between its checks and its opening, and streamed through AGE encryption to `artifact.age`; a failing script, or a missing, empty, oversized, or out-of-snapshot artifact, produces no `artifact.age` and fails the run. The upload allowlists exactly `test.log.age`, `report.md.age`, and `artifact.age`. The CLI streams the archive to disk, checks GitHub's SHA-256 digest, accepts only those three members, decrypts `artifact.age` only for a successful run, and writes it to its destination only after AGE has authenticated the whole plaintext.

AGE protects artifact confidentiality and integrity after encryption. It does not hide plaintext from the active runner or make output authentic against malicious authorized workflow code.

In TestFlight mode the unsigned IPA is encrypted to a separate transport
recipient whose identity is available only inside `apple-production`. The
protected job downloads that ciphertext, rejects traversal, symlinks, special
files, multiple apps, and embedded applications other than `PlugIns` app
extensions. Each extension's Bundle ID must extend the app's, which also keeps a
project from steering the job into registering or provisioning an arbitrary
identifier; at most 16 are accepted. It sets a GitHub-run-derived
`CFBundleVersion` on the app and every extension, signs each extension with its
own App Store profile and then the app, without executing either,
validates the signed IPA with App Store Connect, and uploads it directly to Apple
before deleting it with the ephemeral runner; only an AGE-encrypted diagnostic log is
uploaded to GitHub.

### Hostile or careless test script

A test script is project code with the runner's environment, minus the variables above. It cannot accidentally publish through the job summary, outputs, environment, `PATH`, or workflow commands, and it holds no repository token, runtime token, OIDC token, or Apple credential. A deliberately hostile script can still do what any hostile build phase can: find the runner's file-command files under the runner's temporary directory, modify actions that later steps run, open network connections, or start a process outside its process group or, on Windows, outside its job object (through a service, a scheduled task, or WMI, which an administrator can use). The test jobs have no Environment and no signing job, and the App token is revoked before the script starts, so such a script reaches nothing a build phase could not. The artifact it returns is encrypted only to the caller, so a hostile script can make the caller download a hostile file, never publish one. Treat the test script, and the artifact, with the same review as other build code.

### Leftover public runs

A finished test run's public page shows its dispatch inputs, step names, and fixed status lines, and its encrypted artifact is retained for one day. After the CLI has downloaded and decrypted every output of a run whose tests passed, it deletes the artifact and then the run, which also removes its logs; `--keep-run` opts out. A run whose tests did not pass, or whose outputs could not be retrieved, is deliberately kept for inspection and retrieval until the artifact expires and the repository's log retention removes the run. Deletion uses the caller's own GitHub token, which needs write access to the builder (the `repo` scope, or Actions read and write for a fine-grained token); if it fails the CLI warns with the run URL and leaves the test result unchanged. An interrupted CLI leaves the run behind as well.

### Apple credential compromise

The distribution certificate, its password, provisioning profile bundle, App Store
Connect key, and transport AGE identity are Environment secrets. Secret values
are removed from the trusted runner's environment before child processes start,
sensitive command arguments are not written to diagnostics, and credential
files/keychains live only under runner-temporary paths. Required-reviewer and
branch restrictions are mandatory operational controls. Revoke Apple or AGE
credentials immediately after suspected disclosure; encryption at rest does not
repair a previously exposed credential.

### Concurrent build confusion

Build IDs are full random UUIDs. Run lookup uses the exact run title and dispatch workflow, and artifact lookup is restricted to that run with the build ID in its exact name. The CLI rejects unexpected ZIP members and validates decrypted IPA structure.

### Snapshot leaks and stale refs

Snapshots live only under `refs/ios-builder/jobs/<uuid>` in the configured private repository. Creation uses an alternate Git index, respects `.gitignore`, and leaves branch/index/working tree untouched. Cleanup uses the observed SHA as a lease so it cannot delete a replaced ref. `builder cleanup` removes refs older than 24 hours. An abrupt process kill can still prevent immediate cleanup; scheduled/manual cleanup remains necessary. Untracked non-ignored secrets are included by design, so projects must maintain `.gitignore` carefully.

### Dependency and supply-chain compromise

Pinned Actions reduce tag-retargeting risk. Package managers and private project dependencies still execute code and may contact networks. Lockfiles, dependency review, provenance controls, and minimal private-project install scripts remain the source project's responsibility. No dependency cache crosses builds.

### Metadata disclosure

Public workflow metadata/inputs can expose source owner/repository names, iOS path, scheme, test script path, Windows artifact path, and snapshot ref even though source contents and outputs are encrypted, until a passing test run is deleted. Do not use this backend when repository identity itself is confidential. An opaque broker would be required to hide that metadata.

## Explicit non-goals

- Protecting source from GitHub's runner/control plane.
- Safely building intentionally malicious private projects in a strong sandbox.
- Automatic provisioning and multi-profile signing for Watch apps, App Clips, XPC services, or ExtensionKit extensions.
- Hiding Apple credentials from GitHub's protected signing runner/control plane.
- Guaranteeing GitHub policy approval; see [COMPLIANCE.md](../COMPLIANCE.md).
