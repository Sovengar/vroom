#!/usr/bin/env bash
#
# setup-repo-protection.sh — idempotently configure the `protect-<default-branch>`
# branch ruleset, the repo merge setting, and the labels dependabot.yml references.
#
# The protected branch and the ruleset name are DERIVED FROM THE REPO, never
# hardcoded: the default branch is read from `gh repo view --json
# defaultBranchRef`, and the ruleset is named `protect-<branch>` (for a repo
# whose default branch is `main` that is `protect-main`). Hardcoding `main` on a
# repo whose default branch is something else would silently protect a branch
# that does not exist.
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
# BYPASS — accepted consequence, deliberate; do not "fix": the repo-admin role
# keeps `bypass_mode: always`. The admin can therefore merge red PRs and push or
# force-push the protected branch, bypassing every rule above. The gate is
# absolute only for non-admin actors. Strict mode (no escape hatch) = remove
# `bypass_actors` and temporarily disable the ruleset for hotfixes.
#
# Labels: dependabot.yml references `dependencies` and `ci`. GitHub silently
# drops undefined labels, so this script creates them when missing (idempotent;
# a renamed label is recreated under the new name).
#
# Requires: gh (authenticated, repo admin) and jq.
#
# Usage:
#   scripts/setup-repo-protection.sh [--dry-run] [--contexts Build,Lint,Test] [--sha <commit>]
#
# Env overrides:
#   RULESET_NAME=protect-<branch>   BRANCH=<default branch>   GH_ACTIONS_APP_ID=15368

set -euo pipefail

# GitHub Actions is the integration that reports our CI check runs.
GH_ACTIONS_APP_ID="${GH_ACTIONS_APP_ID:-15368}"
REQUIRED_DEFAULT=(Build Lint Test)

# Labels referenced by .github/dependabot.yml, as "name|color|description".
LABELS=(
  "dependencies|0366d6|Dependency updates"
  "ci|0e8a16|CI / build pipeline"
)

DRY_RUN=0
OPT_CONTEXTS=""
OPT_SHA=""

usage() {
  # Print the leading comment block (everything after the shebang).
  awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
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

# The protected branch is the repo's ACTUAL default branch, not a guess.
DEFAULT_BRANCH="$(gh repo view --json defaultBranchRef -q .defaultBranchRef.name 2>/dev/null || true)"
if [ -z "$DEFAULT_BRANCH" ] || [ "$DEFAULT_BRANCH" = "null" ]; then
  echo "ERROR: could not resolve the default branch for ${REPO}." >&2
  exit 1
fi

BRANCH="${BRANCH:-$DEFAULT_BRANCH}"
RULESET_NAME="${RULESET_NAME:-protect-${BRANCH}}"

# find_ruleset_id prints the id of the ruleset named RULESET_NAME, or nothing.
# The list is paginated (per_page=100) so a large collection cannot make us miss
# the existing ruleset and create a duplicate. A gh failure is fatal — a
# transient error must never be read as "no ruleset exists". `jq -s` slurps the
# concatenated per-page arrays that `gh api --paginate` emits.
find_ruleset_id() {
  local json
  if ! json="$(gh api "repos/${REPO}/rulesets?per_page=100" --paginate)"; then
    echo "ERROR: could not list rulesets for ${REPO}." >&2
    exit 1
  fi
  printf '%s' "$json" | jq -s -r --arg name "$RULESET_NAME" \
    '[.[][] | select(.name == $name) | .id] | first // empty'
}

# ensure_label NAME COLOR DESCRIPTION — create the label only if it is missing,
# so renames are picked up and existing labels are left untouched.
ensure_label() {
  local name="$1" color="$2" desc="$3" existing=""
  if existing="$(gh api "repos/${REPO}/labels/${name}" --jq '.name' 2>/dev/null)" && [ -n "$existing" ]; then
    echo "==> Label '${name}' already exists"
    return 0
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "--- would POST repos/${REPO}/labels { name: ${name}, color: ${color} } ---"
    return 0
  fi
  gh api -X POST "repos/${REPO}/labels" \
    -f "name=${name}" -f "color=${color}" -f "description=${desc}" >/dev/null
  echo "==> Label '${name}' created"
}

# ensure_labels applies the LABELS table.
ensure_labels() {
  local spec name color desc
  for spec in "${LABELS[@]}"; do
    IFS='|' read -r name color desc <<< "$spec"
    ensure_label "$name" "$color" "$desc"
  done
}

echo "==> Repository: ${REPO}"
echo "==> Default branch: ${DEFAULT_BRANCH}"
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
#
# NOTE on `bypass_actors` below: the repo-admin role (actor_id 5) keeps
# `bypass_mode: "always"` on purpose — it is the owner's approved escape hatch.
# Accepted consequence (deliberate): an admin can merge red PRs and push or
# force-push the protected branch, so the ruleset is absolute only for non-admin
# actors. Strict enforcement (no escape hatch) = drop `bypass_actors` and
# disable the ruleset explicitly during a hotfix.
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
        "required_review_thread_resolution": false,
        "require_extra_approval_for_unattributed_changes": true,
        "allowed_merge_methods": ["merge", "squash", "rebase"]
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
existing_id="$(find_ruleset_id)"

if [ "$DRY_RUN" -eq 1 ]; then
  echo "==> DRY RUN — no mutations will be performed."
  if [ -n "$existing_id" ]; then
    echo "--- would PUT repos/${REPO}/rulesets/${existing_id} ---"
  else
    echo "--- would POST repos/${REPO}/rulesets ---"
  fi
  printf '%s\n' "$payload"
  echo "--- would PATCH repos/${REPO} { \"delete_branch_on_merge\": true } ---"
  ensure_labels
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

ruleset_id="$(find_ruleset_id)"
if [ -z "$ruleset_id" ]; then
  echo "ERROR: ruleset '${RULESET_NAME}' not found after write." >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# 6. Repo setting: delete the head branch once a PR is merged.
# ---------------------------------------------------------------------------
gh api -X PATCH "repos/${REPO}" -f delete_branch_on_merge=true >/dev/null
echo "==> Repo setting delete_branch_on_merge=true applied"

# ---------------------------------------------------------------------------
# 6b. Dependabot labels — created only when missing (idempotent).
# ---------------------------------------------------------------------------
ensure_labels

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
echo
echo "labels:"
for spec in "${LABELS[@]}"; do
  name="${spec%%|*}"
  if ! gh api "repos/${REPO}/labels/${name}" --jq '"  - \(.name) #\(.color) — \(.description)"' 2>/dev/null; then
    echo "  - ${name} MISSING"
  fi
done
echo "================================================="
echo "==> Done. ruleset_id=${ruleset_id}"
