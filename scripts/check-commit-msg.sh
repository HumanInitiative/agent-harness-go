#!/usr/bin/env bash
# Validates one commit message against Conventional Commits as described in
# CONTRIBUTING.md.
#
# Usage: scripts/check-commit-msg.sh <file-containing-the-message>
#
# Set STRICT=1 (CI does) to also reject fixup!/squash!/amend! commits, which
# are fine while a branch is in review but must be autosquashed before merge.
set -euo pipefail

file="${1:?usage: $0 <commit-message-file>}"
strict="${STRICT:-0}"

types='feat|fix|refactor|perf|docs|test|chore|ci|build|revert'
# type(optional-scope)!: description — description must not start with an
# uppercase letter or end with a period.
pattern="^(${types})(\([a-z0-9-]+\))?!?: [^A-Z[:space:]].*[^.[:space:]]$"
max_subject=100
recommended_subject=72

# Drop comment lines git adds to the editor template, then trailing blanks.
message="$(grep -v '^#' "$file" | sed -e :a -e '/^[[:space:]]*$/{$d;N;ba' -e '}')"
subject="$(printf '%s\n' "$message" | head -n1)"
second_line="$(printf '%s\n' "$message" | sed -n 2p)"

fail() {
  cat >&2 <<EOF
✗ Invalid commit message: "$subject"
  $1

  Expected: <type>(<optional-scope>): <description>
    feat(tools): add web search tool
    fix(httpapi): return 504 instead of 500 on timeout
    refactor!: rename ChatInput to TurnInput

  <type> is one of: ${types//|/, }
  See CONTRIBUTING.md#commit-messages
EOF
  exit 1
}

case "$subject" in
  "Revert \""*) exit 0 ;; # git revert's default subject
  "fixup! "* | "squash! "* | "amend! "*)
    if [[ "$strict" == 1 ]]; then
      fail "fixup/squash commits must be squashed before merging: git rebase -i --autosquash origin/main"
    fi
    exit 0
    ;;
  "Merge "*)
    fail "merge commits are not allowed; rebase onto main instead: git rebase origin/main"
    ;;
esac

[[ -n "$subject" ]] || fail "the message is empty"
[[ "$subject" =~ $pattern ]] || fail "the subject does not follow Conventional Commits"
(( ${#subject} <= max_subject )) || fail "the subject is ${#subject} characters; the limit is ${max_subject} (aim for ${recommended_subject})"
[[ -z "$second_line" ]] || fail "separate the subject from the body with a blank line"
