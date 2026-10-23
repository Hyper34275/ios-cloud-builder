# Privacy rules for this public repository

This repository is public, and so is everything around it: commits and their
author emails, pull request titles and bodies, Actions run logs, step
headers, job outputs, and artifact names. It also runs builds of private
projects. Every rule below exists because breaking it once published
something private. `scripts/privacy-check.sh` enforces what can be checked
mechanically, and the static workflow tests in `internal/runner` enforce the
workflow rules.

## Commits

- Commit with your GitHub noreply address
  (`<id>+<login>@users.noreply.github.com`), never a personal email. Set it
  once with `git config --global user.email <noreply address>`, and turn on
  *Keep my email addresses private* and *Block command line pushes that expose
  my email* under GitHub → Settings → Emails.
- Never name a private project, app, bundle ID, Apple team, test script or
  file from a private repository in code, tests, docs or commit messages. Use
  `com.example.*`, `App`, `AppSetup.exe`, `private-app` and similar.
- No local paths (`/home/<you>/…`, `/Users/<you>/…`), hostnames, IPs, or
  personal domains.
- History is permanent: GitHub keeps every pull request's commits under
  `refs/pull/*` even after a force-push. A leak must be stopped before it is
  pushed, not cleaned up later.

## Pull requests

- Describe the change generically. Write "a watch app's bundle ID" rather
  than the ID itself, and "a private project" rather than its name.
- Never paste build logs, crash logs, profile names, App Store Connect output
  or screenshots from a private project.
- The *Privacy* workflow (`.github/workflows/privacy.yml`) checks the title,
  body, commits and added lines of every pull request, whatever its base.
  Editing the title or body reruns it. A pull request from a fork gets no
  secrets, so private names are not checked there; a maintainer must review
  it by hand.

## The central workflow

Public step headers print every `env:` and `with:` value that is not a
secret. When you change `.github/workflows/ios-build.yml` (and its identical
copy in `internal/workflow/templates/`):

- A step that receives a private input (`source_owner`, `source_repo`,
  `ios_path`, `scheme`, `test_script`, `artifact_path`) must come after
  *Mask private dispatch inputs*. A new private input must be added to
  `maskedInputs` in `internal/runner/privacy.go`.
- Never interpolate `${{ … }}` into a `run:` script; pass values through
  `env:`.
- Store anything that identifies an account or project as a **secret**, not a
  variable. Variables are printed in clear. Only `APP_CLIENT_ID` and
  `APPLE_SIGNING_RECIPIENT` are variables, because they are public by design.
- Never `echo` a value derived from private source (commit counts, file
  names, versions). Pass it between jobs as an AGE-encrypted artifact, as the
  sealed TestFlight build number is. Do not use job outputs: they are not
  encrypted, and GitHub silently drops an output that happens to contain a
  masked value.
- Project output goes only to the private log, which is encrypted. The public
  log carries fixed status lines only. Never add `GITHUB_STEP_SUMMARY`,
  annotations or job outputs that carry project data.
- Upload only `.age` ciphertext, by exact path, with `retention-days: 1`.

## Flutter `--dart-define` values and other configuration

Values compiled into an app (`dartDefines`, API endpoints, keys) must never
travel as dispatch inputs, because inputs reach step headers. Ship them
inside the private snapshot.

## Before every push

```bash
go test ./...
scripts/privacy-check.sh origin/main
```

To check private names locally too, export `PRIVACY_DENYLIST` with one name
per line. The CI job reads the same list from the `PRIVACY_DENYLIST`
repository secret, so the list is never published.

## If something leaks anyway

1. Revoke or rotate any credential involved first. Hiding it does not undo
   exposure.
2. Delete affected Actions runs (`gh run delete <id>`). This removes their
   logs.
3. Edit the pull request title, body or comments.
4. Commits already pushed cannot be fully removed: `refs/pull/*` keeps them.
   Only GitHub Support can purge them.
