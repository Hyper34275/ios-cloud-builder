# Upstream relationship

`ios-cloud-builder` is derived from [MobAI-App/ios-builder](https://github.com/MobAI-App/ios-builder) under the MIT license. The central-backend work began from upstream `v0.7.0`, commit `bd80043f530366ef61392697f92dbd70fe24ad81` (2026-08-22). The original license and history are retained.

Upstream functionality retained includes the repository-local build backend, alternate-index working-tree snapshots, framework and iOS project detection, unsigned and repository-local signed builds, simulator sharing, MobAI integration, Flutter/React Native/KMP development commands, CSR/P12 tooling, release packaging, and public Go wrapper packages.

This derivative adds a backend/config separation between private source and public execution repositories, scoped GitHub App checkout, AGE-encrypted artifacts and diagnostics, public-log isolation, stale snapshot cleanup, central setup/doctor commands, and the security/compliance model.

## Updating from upstream

The remotes should remain:

```text
origin    git@github.com:ori2015/ios-cloud-builder.git
upstream  https://github.com/MobAI-App/ios-builder.git
```

Update without rewriting upstream history:

```bash
git fetch upstream --tags
git switch main
git merge upstream/main
go test ./...
go test -race ./...
go vet ./...
```

Resolve conflicts by preserving the modular central backend and retaining upstream repository-mode behavior. Avoid formatting churn so security-sensitive differences remain reviewable.
