#!/usr/bin/env bash
# Validates every commit in <base>..<head>: no merge commits, and each
# message passes scripts/check-commit-msg.sh in strict mode. With
# rebase-and-merge, every one of these commits lands on main unchanged, so
# every one of them has to be clean.
#
# Usage: scripts/check-commits.sh <base> <head>
#   local: scripts/check-commits.sh origin/main HEAD
set -euo pipefail

base="${1:?usage: $0 <base> <head>}"
head="${2:?usage: $0 <base> <head>}"
here="$(cd "$(dirname "$0")" && pwd)"

merges="$(git rev-list --merges "$base..$head")"
if [[ -n "$merges" ]]; then
  echo "✗ Merge commits are not allowed (keep history linear; rebase onto main):" >&2
  git log --oneline --merges "$base..$head" >&2
  exit 1
fi

commits="$(git rev-list --reverse "$base..$head")"
if [[ -z "$commits" ]]; then
  echo "No commits to check in $base..$head."
  exit 0
fi

msg_file="$(mktemp)"
trap 'rm -f "$msg_file"' EXIT

status=0
for sha in $commits; do
  git log -1 --format=%B "$sha" >"$msg_file"
  if ! STRICT=1 "$here/check-commit-msg.sh" "$msg_file"; then
    echo "  ↳ in commit $(git log -1 --format='%h' "$sha")" >&2
    status=1
  fi
done

if [[ "$status" == 0 ]]; then
  echo "✓ $(wc -l <<<"$commits" | tr -d ' ') commit(s) follow the convention."
fi
exit "$status"
