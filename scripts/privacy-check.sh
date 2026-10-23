#!/usr/bin/env bash
# Fails when a change would publish something private through this public
# repository. Usage: scripts/privacy-check.sh <base-ref> [<head-ref>]
#
# Checked: the author and committer email of every new commit, the lines the
# change adds, the new commit messages, and (in CI) the pull request title and
# body. Private identifiers come from PRIVACY_DENYLIST (one per line, matched
# case-insensitively), which CI reads from a repository secret so the list
# itself is never published. Findings name only the file and line, never the
# matched text.
set -euo pipefail

base=${1:?usage: privacy-check.sh <base-ref> [<head-ref>]}
head=${2:-HEAD}
range="$base..$head"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0

fail() {
    echo "privacy: $*" >&2
    failures=$((failures + 1))
}

# Commit emails that may appear publicly: GitHub noreply addresses and the
# upstream authors whose addresses upstream history already publishes.
allowed_email='(@users\.noreply\.github\.com|^noreply@github\.com|^contact@mobai\.run|^mobai\.app\.run@gmail\.com|^ori1598@gmail\.com)$'
while read -r commit email; do
    if ! grep -Eq "$allowed_email" <<<"$email"; then
        fail "commit ${commit:0:12} uses a personal email; use your users.noreply.github.com address"
    fi
done < <(git log --format='%H %ae%n%H %ce' "$range")

# Added lines, as "file<TAB>line<TAB>text".
git diff --unified=0 --no-color "$range" -- . ':(exclude)scripts/privacy-check.sh' | awk '
    /^\+\+\+ / { file = substr($0, 7); next }
    /^@@/ { split($3, range, ","); line = substr(range[1], 2) + 0; next }
    /^\+/ { printf "%s\t%d\t%s\n", file, line, substr($0, 2); line++ }
' >"$work/added"
git log --format='%H%n%B' "$range" >"$work/messages"
printf '%s\n%s\n' "${PR_TITLE:-}" "${PR_BODY:-}" >"$work/pull-request"

# check <label> <grep args...> reports each added line, commit message and
# pull request field that matches, by location only.
check() {
    local label=$1 matches
    shift
    matches=$({ cut -f3- "$work/added" | grep -n "$@" || true; } | cut -d: -f1)
    for index in $matches; do
        fail "$label in $(sed -n "${index}p" "$work/added" | cut -f1,2 | tr '\t' ':')"
    done
    if grep -q "$@" "$work/messages"; then
        fail "$label in a commit message"
    fi
    if grep -q "$@" "$work/pull-request"; then
        fail "$label in the pull request title or body"
    fi
}

check "credential-shaped token" -E 'gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|AGE-SECRET-KEY-1[0-9A-Z]{50,}|AKIA[0-9A-Z]{16}|BEGIN [A-Z ]*PRIVATE KEY-----[A-Za-z0-9+/=]{40,}'
check "personal email address" -Ei '[a-z0-9._%+-]+@(gmail|googlemail|outlook|hotmail|live|yahoo|icloud|me|proton|protonmail)\.[a-z.]+'
check "personal home directory" -P '/(home|Users)/(?!runner/|user/|username/|example/|you/|me/|name/)[a-z][a-z0-9_-]*/'

if [ -n "${PRIVACY_DENYLIST:-}" ]; then
    printf '%s\n' "$PRIVACY_DENYLIST" | sed '/^[[:space:]]*$/d' >"$work/denylist"
    if [ -s "$work/denylist" ]; then
        check "private identifier from PRIVACY_DENYLIST" -iF -f "$work/denylist"
    fi
else
    echo "privacy: PRIVACY_DENYLIST is not set; private names were not checked" >&2
fi

if [ "$failures" -ne 0 ]; then
    echo "privacy: $failures problem(s); see docs/PRIVACY.md" >&2
    exit 1
fi
echo "privacy: no private data found in $range"
