#!/usr/bin/env bash
#
# setup-repo-protection.sh — idempotently configure the `protect-main` branch
# ruleset plus the repo setting that keeps main safe.
#
# Policy (see the plan / PR description for the rationale):
#   - block branch deletion            (`deletion`)
#   - block force-pushes               (`non_fast_forward`)
#   - require a pull request to merge  (`pull_request`, 0 approvals -> solo dev)
#   - require the CI status checks     (`required_status_checks`, strict = false)
#
# `strict_required_status_checks_policy` is deliberately false: it requires the
# branch to be up to date before merging, which would force a rebase on every
# concurrent PR. We do not want forced rebases.
#
# The required check contexts are DERIVED FROM REALITY: they are read from the
# check runs of the most recently updated PR head, so the gate can never be
# configured against check names that do not exist.
#
# The repo-admin role is granted an always-on bypass: the owner can still push
# to main directly (approved escape hatch) without weakening the gate for PRs.
#
# Requires: gh (authenticated, repo admin) and jq.
#
# Usage:
#   scripts/setup-repo-protection.sh [--dry-run] [--contexts Build,Lint,Test] [--sha <commit>]
#
# Env overrides:
#   RULESET_NAME=protect-main   BRANCH=main   GH_ACTIONS_APP_ID=15368

set -euo pipefail

RULESET_NAME="${RULESET_NAME:-protect-main}"
BRANCH="${BRANCH:-main}"
# GitHub Actions is the integration that reports our CI check runs.
GH_ACTIONS_APP_ID="${GH_ACTIONS_APP_ID:-15368}"
REQUIRED_DEFAULT=(Build Lint Test)

DRY_RUN=0
OPT_CONTEXTS=""
OPT_SHA=""

usage() {
  sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    --contexts) OPT_CONTEXTS="${2:-}"; shift 2 ;;
    --sha) OPT_SHA="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

for bin in gh jq; do
  command -v "$bin" >/dev/null 2>&1 || { echo "ERROR: '$bin' is required but not installed." >&2; exit 1; }
done

REPO="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
[ -n "$REPO" ] || { echo "ERROR: could not resolve owner/repo (run inside the repository)." >&2; exit 1; }

echo "==> Repository: ${REPO}"
echo "==> Ruleset:    ${RULESET_NAME} (target=refs/heads/${BRANCH}, enforcement=active)"

# ---------------------------------------------------------------------------
# 1. Verify the GitHub Actions app id instead of trusting the hardcoded value.
# ---------------------------------------------------------------------------
actual_app_id="$(gh api /apps/github-actions --jq '.id')"
if [ "$actual_app_id" != "$GH_ACTIONS_APP_ID" ]; then
  echo "ERROR: GitHub Actions app id is '${actual_app_id}', expected '${GH_ACTIONS_APP_ID}'." >&2
  echo "       Update GH_ACTIONS_APP_ID to match before proceeding." >&2
  exit 1
fi
echo "==> GitHub Actions app id verified: ${GH_ACTIONS_APP_ID}"

# ---------------------------------------------------------------------------
# 2. Resolve the required status check contexts.
#    Default: derive them from the latest PR head's real check runs.
# ---------------------------------------------------------------------------
contexts=()
if [ -n "$OPT_CONTEXTS" ]; then
  IFS=',' read -r -a names <<< "$OPT_CONTEXTS"
  for name in "${names[@]}"; do
    name="$(printf '%s' "$name" | xargs)"
    [ -n "$name" ] && contexts+=("$name")
  done
  echo "==> Check contexts (explicit override): ${contexts[*]}"
else
  sha="${OPT_SHA}"
  if [ -z "$sha" ]; then
    sha="$(gh api "repos/${REPO}/pulls?state=all&sort=updated&direction=desc&per_page=1" --jq '.[0].head.sha' 2>/dev/null || true)"
  fi
  if [ -z "$sha" ] || [ "$sha" = "null" ]; then
    echo "ERROR: no PR head SHA available to derive required check names from." >&2
    echo "       Open a PR whose CI has run, or pass --sha <commit> / --contexts Build,Lint,Test." >&2
    exit 1
  fi
  echo "==> Deriving contexts from check runs of ${sha}"
  observed="$(gh api "repos/${REPO}/commits/${sha}/check-runs?per_page=100" --jq '.check_runs[].name' 2>/dev/null || true)"
  if [ -z "$observed" ]; then
    echo "ERROR: no check runs found on ${sha}. CI may not have run yet." >&2
    exit 1
  fi
  for req in "${REQUIRED_DEFAULT[@]}"; do
    match="$(printf '%s\n' "$observed" | grep -Fx "$req" | head -n1 || true)"
    if [ -z "$match" ]; then
      echo "ERROR: expected check '${req}' not found on ${sha}." >&2
      echo "       Observed check runs:" >&2
      while IFS= read -r line; do
        [ -n "$line" ] && printf '         %s\n' "$line" >&2
      done <<< "$observed"
      echo "       Refusing to configure a ruleset against non-existent checks." >&2
      exit 1
    fi
    contexts+=("$match")
  done
  echo "==> Check contexts (derived from observed runs): ${contexts[*]}"
fi

if [ "${#contexts[@]}" -eq 0 ]; then
  echo "ERROR: no status check contexts resolved." >&2
  exit 1
fi

# Build the required_status_checks array, pinning every context to the
# GitHub Actions integration so only Actions-reported checks satisfy the gate.
checks_json="["
first=1
for ctx in "${contexts[@]}"; do
  if [ "$first" -eq 0 ]; then checks_json+=","; fi
  first=0
  checks_json+="{\"context\":\"${ctx}\",\"integration_id\":${GH_ACTIONS_APP_ID}}"
done
checks_json+="]"

# ---------------------------------------------------------------------------
# 3. Build the ruleset payload.
# ---------------------------------------------------------------------------
payload="$(cat <<JSON
{
  "name": "${RULESET_NAME}",
  "target": "branch",
  "enforcement": "active",
  "conditions": {
    "ref_name": {
      "include": ["refs/heads/${BRANCH}"],
      "exclude": []
    }
  },
  "bypass_actors": [
    {
      "actor_id": 5,
      "actor_type": "RepositoryRole",
      "bypass_mode": "always"
    }
  ],
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    {
      "type": "pull_request",
      "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": false,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": false
      }
    },
    {
      "type": "required_status_checks",
      "parameters": {
        "strict_required_status_checks_policy": false,
        "do_not_enforce_on_create": false,
        "required_status_checks": ${checks_json}
      }
    }
  ]
}
JSON
)"

# Fail fast if the generated JSON is malformed.
printf '%s' "$payload" | jq -e . >/dev/null

# ---------------------------------------------------------------------------
# 4. Find an existing ruleset with this name (idempotency).
# ---------------------------------------------------------------------------
existing_id="$(gh api "repos/${REPO}/rulesets" --jq ".[] | select(.name == \"${RULESET_NAME}\") | .id" 2>/dev/null | head -n1 || true)"

if [ "$DRY_RUN" -eq 1 ]; then
  echo "==> DRY RUN — no mutations will be performed."
  if [ -n "$existing_id" ]; then
    echo "--- would PUT repos/${REPO}/rulesets/${existing_id} ---"
  else
    echo "--- would POST repos/${REPO}/rulesets ---"
  fi
  printf '%s\n' "$payload"
  echo "--- would PATCH repos/${REPO} { \"delete_branch_on_merge\": true } ---"
  exit 0
fi

# ---------------------------------------------------------------------------
# 5. Create or update the ruleset (JSON on stdin).
# ---------------------------------------------------------------------------
if [ -n "$existing_id" ]; then
  echo "==> Updating existing ruleset id=${existing_id}"
  printf '%s' "$payload" | gh api -X PUT "repos/${REPO}/rulesets/${existing_id}" --input - >/dev/null
else
  echo "==> Creating ruleset"
  printf '%s' "$payload" | gh api -X POST "repos/${REPO}/rulesets" --input - >/dev/null
fi

ruleset_id="$(gh api "repos/${REPO}/rulesets" --jq ".[] | select(.name == \"${RULESET_NAME}\") | .id" | head -n1)"
[ -n "$ruleset_id" ] || { echo "ERROR: ruleset '${RULESET_NAME}' not found after write." >&2; exit 1; }

# ---------------------------------------------------------------------------
# 6. Repo setting: delete the head branch once a PR is merged.
# ---------------------------------------------------------------------------
gh api -X PATCH "repos/${REPO}" -f delete_branch_on_merge=true >/dev/null
echo "==> Repo setting delete_branch_on_merge=true applied"

# ---------------------------------------------------------------------------
# 7. Audit summary — read the ruleset back from the API.
# ---------------------------------------------------------------------------
readback="$(gh api "repos/${REPO}/rulesets/${ruleset_id}")"
echo
echo "================= RULESET AUDIT ================="
printf '%s' "$readback" | jq -r '
  "id:          \(.id)",
  "name:        \(.name)",
  "target:      \(.target)",
  "enforcement: \(.enforcement)",
  "include:     \(.conditions.ref_name.include | join(", "))",
  "",
  "rules:"
'
printf '%s' "$readback" | jq -r '
  .rules[] |
  if .type == "required_status_checks" then
    "  - \(.type) (strict=\(.parameters.strict_required_status_checks_policy)): " +
    ([.parameters.required_status_checks[] | .context] | join(", "))
  elif .type == "pull_request" then
    "  - \(.type) (required_approving_review_count=\(.parameters.required_approving_review_count))"
  else
    "  - \(.type)"
  end
'
echo
echo "bypass actors:"
printf '%s' "$readback" | jq -r '.bypass_actors[]? | "  - \(.actor_type) actor_id=\(.actor_id) mode=\(.bypass_mode)"'
echo "================================================="
echo "==> Done. ruleset_id=${ruleset_id}"
