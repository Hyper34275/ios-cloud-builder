# Threat model

## Security goals

Central mode is designed so a public repository never commits private application source, uploads a plaintext private IPA or detailed build log, stores a decryption identity, stores Apple signing material, or keeps a persistent private-repository credential. The local CLI pushes the working tree only to a temporary ref in the private source repository. A repository-scoped GitHub App installation token performs the private checkout. Only AGE ciphertext is uploaded.

## Trust boundaries

- The local workstation and its AGE identity are trusted.
- The private source repository and its write-authorized operator are trusted to initiate builds.
- GitHub's runner and Actions control plane necessarily see plaintext while the job runs.
- Public Actions logs, artifacts, caches, inputs, summaries, annotations, and public pull requests are untrusted/public territory.
- Maintainers with write/admin access to the public builder are highly trusted. They can modify the workflow or helper and can dispatch builds. Keep this group minimal.
- Private application build scripts and dependencies execute arbitrary code on the runner. Environment scrubbing prevents accidental access to known Actions channels and tokens, but is not a sandbox and cannot prevent all network exfiltration by a malicious project.

## Threats and mitigations

### Malicious public pull request

The private-source workflow is `workflow_dispatch` only. PR CI has read-only permissions and no central secrets. External actions are pinned to full commit SHAs. CODEOWNERS identifies security-sensitive paths. Default-branch protection and required review must also be enabled in repository settings.

### Workflow modification or hostile maintainer

An authorized writer could change the trusted helper, select a private repository installed for the App, or substitute their own AGE recipient. Protect `main`, require review for `.github/workflows/ios-build.yml`, `internal/runner`, `internal/security`, and central coordination code, and audit dispatches. Repository administrators remain trusted because they can bypass repository controls.

### Stolen GitHub App private key

The App has only Metadata read and Contents read, and is installed using **Only select repositories**. Every workflow requests a token for exactly one validated repository. Rotate the App key and review installations immediately after suspected compromise. A stolen key can read every selected repository until revoked; scoping a single job does not change the App installation's overall blast radius.

### Token persistence and project-code access

Both checkouts use `persist-credentials: false`. The App token is passed only to the private checkout and is explicitly revoked before dependency or build code runs. The build helper removes GitHub/Actions token and file-command variables from child environments. The workflow must never print environment variables, remotes, tokens, or secret values.

### Input, expression, path, and shell injection

The trusted Go helper validates UUID, owner, repository, exact snapshot ref, relative iOS path, scheme, configuration, framework enum, and X25519 recipient before token minting. Private paths are resolved after checkout and must remain inside the source root. Build processes receive fixed argv arrays; there is no `eval`, arbitrary command, arbitrary script, or generic shell input.

### Artifact, log, and cache disclosure

Compiler/dependency output starts redirected into a private build log. Both IPA and log are AGE-encrypted before upload, plaintext files are deleted, and the artifact step uses an exact ciphertext allowlist with one-day retention. The CLI binds the artifact to the exact run/build UUID, decrypts locally, validates IPA structure, and attempts remote artifact deletion. Central mode uses no Actions cache and never uploads DerivedData, dSYMs, archives, source, or plaintext diagnostics.

AGE protects artifact confidentiality and integrity after encryption. It does not hide plaintext from the active runner or make output authentic against malicious authorized workflow code.

### Concurrent build confusion

Build IDs are full random UUIDs. Run lookup uses the exact run title and dispatch workflow, and artifact lookup is restricted to that run with the build ID in its exact name. The CLI rejects unexpected ZIP members and validates decrypted IPA structure.

### Snapshot leaks and stale refs

Snapshots live only under `refs/ios-builder/jobs/<uuid>` in the configured private repository. Creation uses an alternate Git index, respects `.gitignore`, and leaves branch/index/working tree untouched. Cleanup uses the observed SHA as a lease so it cannot delete a replaced ref. `builder cleanup` removes refs older than 24 hours. An abrupt process kill can still prevent immediate cleanup; scheduled/manual cleanup remains necessary. Untracked non-ignored secrets are included by design, so projects must maintain `.gitignore` carefully.

### Dependency and supply-chain compromise

Pinned Actions reduce tag-retargeting risk. Package managers and private project dependencies still execute code and may contact networks. Lockfiles, dependency review, provenance controls, and minimal private-project install scripts remain the source project's responsibility. No dependency cache crosses builds.

### Metadata disclosure

Public workflow metadata/inputs can expose source owner/repository names, iOS path, scheme, and snapshot ref even though source contents and outputs are encrypted. Do not use this backend when repository identity itself is confidential. An opaque broker would be required to hide that metadata.

## Explicit non-goals

- Protecting source from GitHub's runner/control plane.
- Safely building intentionally malicious private projects in a strong sandbox.
- Code signing in central v1.
- Guaranteeing GitHub policy approval; see [COMPLIANCE.md](../COMPLIANCE.md).
