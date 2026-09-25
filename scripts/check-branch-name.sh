#!/usr/bin/env bash
# Validates a branch name against the convention in CONTRIBUTING.md:
#
#   <type>/<short-kebab-description>        feat/add-search-tool
#   <type>/<issue>-<short-kebab-description> fix/42-timeout-on-long-replies
#
# Usage: scripts/check-branch-name.sh <branch-name>
set -euo pipefail

branch="${1:?usage: $0 <branch-name>}"

types='feat|fix|refactor|perf|docs|test|chore|ci|build|revert'
pattern="^(${types})/([0-9]+-)?[a-z0-9]+(-[a-z0-9]+)*$"
max_length=60

case "$branch" in
  main) exit 0 ;;          # the trunk itself
  dependabot/*) exit 0 ;;  # created and named by Dependabot
esac

if [[ ! "$branch" =~ $pattern ]]; then
  cat >&2 <<EOF
✗ Invalid branch name: "$branch"

  Expected: <type>/<short-kebab-description>, optionally with an issue number
    feat/add-search-tool
    fix/42-timeout-on-long-replies

  <type> is one of: ${types//|/, }
  Use lowercase letters, digits and single hyphens only.

  Rename the current branch with:  git branch -m <new-name>
EOF
  exit 1
fi

if (( ${#branch} > max_length )); then
  echo "✗ Branch name is ${#branch} characters; keep it within ${max_length}: \"$branch\"" >&2
  exit 1
fi
