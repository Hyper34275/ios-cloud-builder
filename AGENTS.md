# Project Instructions

- Preserve upstream history and keep `upstream` pointed at `MobAI-App/ios-builder`.
- Central-builder changes must never commit private application source or plaintext private build outputs.
- Keep the original repository backend working while adding central-builder behavior behind clear interfaces.
- Treat public Actions logs, artifacts, caches, inputs, summaries, and annotations as publicly observable.
- Never store credentials, AGE identities, Apple signing material, or persistent private-repository tokens in this repository.
- Run `go test ./...`, `go test -race ./...`, `go vet ./...`, and build the CLI before completion.
