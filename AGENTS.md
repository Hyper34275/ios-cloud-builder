# Project Instructions

- Preserve upstream history and keep `upstream` pointed at `MobAI-App/ios-builder`.
- Central-builder changes must never commit private application source or plaintext private build outputs.
- Keep the original repository backend working while adding central-builder behavior behind clear interfaces.
- Treat public Actions logs, artifacts, caches, inputs, summaries, and annotations as publicly observable.
- Never store credentials, AGE identities, Apple signing material, or persistent private-repository tokens in this repository.
- Run `go test ./...`, `go test -race ./...`, `go vet ./...`, and build the CLI before completion.
- This repository and its Actions logs are public. Follow `docs/PRIVACY.md`: never name private projects, apps, bundle IDs, teams or local paths in code, tests, commits or PR text; commit with a noreply email; run `scripts/privacy-check.sh origin/main` before pushing.
