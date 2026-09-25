#!/usr/bin/env bash
# Applies the GitHub repository settings the workflow in CONTRIBUTING.md
# depends on. Idempotent: safe to re-run after changing anything below.
# Requires the GitHub CLI (`gh`), authenticated as a repository admin.
#
# Usage:
#   scripts/github-setup.sh [owner/repo]
#   REQUIRED_APPROVALS=0 scripts/github-setup.sh   # solo maintainer
#
# REQUIRED_APPROVALS defaults to 1. GitHub never lets authors approve their
# own PRs, so with a single maintainer 1 would block every merge; use 0
# until there is a second reviewer.
set -euo pipefail

repo="${1:-HumanInitiative/agent-harness-go}"
approvals="${REQUIRED_APPROVALS:-1}"
ruleset_name="protect-main"

echo "→ Merge settings for $repo: rebase-and-merge only, auto-delete merged branches"
gh api --silent -X PATCH "repos/$repo" \
  -F allow_merge_commit=false \
  -F allow_squash_merge=false \
  -F allow_rebase_merge=true \
  -F delete_branch_on_merge=true \
  -F allow_update_branch=true

# 15368 is the GitHub Actions app: pinning it means only a real CI run can
# satisfy a required check, not a status posted by some other integration.
ruleset="$(cat <<EOF
{
  "name": "$ruleset_name",
  "target": "branch",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": { "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] } },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "required_linear_history" },
    {
      "type": "pull_request",
      "parameters": {
        "required_approving_review_count": $approvals,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": false,
        "require_last_push_approval": $( ((approvals > 0)) && echo true || echo false ),
        "required_review_thread_resolution": true,
        "allowed_merge_methods": ["rebase"]
      }
    },
    {
      "type": "required_status_checks",
      "parameters": {
        "strict_required_status_checks_policy": true,
        "required_status_checks": [
          { "context": "conventions", "integration_id": 15368 },
          { "context": "lint",        "integration_id": 15368 },
          { "context": "test",        "integration_id": 15368 },
          { "context": "swagger",     "integration_id": 15368 },
          { "context": "build",       "integration_id": 15368 }
        ]
      }
    }
  ]
}
EOF
)"

existing_id="$(gh api "repos/$repo/rulesets" --jq ".[] | select(.name == \"$ruleset_name\") | .id")"
if [[ -n "$existing_id" ]]; then
  echo "→ Updating ruleset '$ruleset_name' (id $existing_id) on the default branch"
  gh api --silent -X PUT "repos/$repo/rulesets/$existing_id" --input - <<<"$ruleset"
else
  echo "→ Creating ruleset '$ruleset_name' on the default branch"
  gh api --silent -X POST "repos/$repo/rulesets" --input - <<<"$ruleset"
fi

echo "✓ Done. Required approvals: $approvals."
