#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-router_for_me}"
FORK_REMOTE="${FORK_REMOTE:-origin}"
MAIN_BRANCH="${MAIN_BRANCH:-main}"
PROD_BRANCH="${PROD_BRANCH:-deploy/prod}"
DEFAULT_SOURCE_REF="${DEFAULT_SOURCE_REF:-feat/codex-oauth-usage-quota}"
DEPLOY_SCRIPT="${DEPLOY_SCRIPT:-}"

usage() {
  cat <<'USAGE'
Usage:
  scripts/workflow.sh status
  scripts/workflow.sh sync-main
  scripts/workflow.sh promote-prod [source-ref]
  scripts/workflow.sh deploy-prod
  scripts/workflow.sh sync-promote-deploy [source-ref]

Defaults:
  source-ref for promote-prod/sync-promote-deploy: feat/codex-oauth-usage-quota
  upstream remote: router_for_me
  fork remote: origin
  main branch: main
  prod branch: deploy/prod

Environment overrides:
  UPSTREAM_REMOTE     default: router_for_me
  FORK_REMOTE         default: origin
  MAIN_BRANCH         default: main
  PROD_BRANCH         default: deploy/prod
  DEFAULT_SOURCE_REF  default: feat/codex-oauth-usage-quota
  DEPLOY_SCRIPT       optional explicit deploy script path
                      default: auto-discover scripts/deploy-*.sh
USAGE
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

require_repo() {
  git -C "$ROOT_DIR" rev-parse --show-toplevel >/dev/null 2>&1 || {
    echo "Not a git repository: $ROOT_DIR" >&2
    exit 1
  }
}

resolve_deploy_script() {
  if [[ -n "$DEPLOY_SCRIPT" ]]; then
    printf '%s' "$DEPLOY_SCRIPT"
    return 0
  fi

  local candidates=()
  local script
  for script in "$ROOT_DIR"/scripts/deploy-*.sh; do
    if [[ -f "$script" && -x "$script" ]]; then
      candidates+=("$script")
    fi
  done

  if [[ ${#candidates[@]} -eq 1 ]]; then
    printf '%s' "${candidates[0]}"
    return 0
  fi

  if [[ ${#candidates[@]} -eq 0 ]]; then
    echo "No deploy script found under scripts/deploy-*.sh. Set DEPLOY_SCRIPT explicitly." >&2
  else
    echo "Multiple deploy scripts found. Set DEPLOY_SCRIPT explicitly." >&2
    printf 'Candidates:
' >&2
    printf '  %s
' "${candidates[@]}" >&2
  fi
  exit 1
}

retry_fetch() {
  local remote="$1"
  if git -C "$ROOT_DIR" -c http.version=HTTP/1.1 fetch "$remote" --prune; then
    return 0
  fi
  git -C "$ROOT_DIR" fetch "$remote" --prune
}

ensure_commitish() {
  local ref="$1"
  git -C "$ROOT_DIR" rev-parse --verify "${ref}^{commit}" >/dev/null 2>&1 || {
    echo "Unknown git ref: $ref" >&2
    exit 1
  }
}

ensure_clean_tracked_tree() {
  if ! git -C "$ROOT_DIR" diff --quiet --ignore-submodules -- . ':(exclude)scripts'; then
    echo "Tracked working tree has local modifications. Commit or discard them before running workflow commands." >&2
    git -C "$ROOT_DIR" status --short --untracked-files=no >&2
    exit 1
  fi
  if ! git -C "$ROOT_DIR" diff --cached --quiet --ignore-submodules -- . ':(exclude)scripts'; then
    echo "Index has staged modifications. Commit or unstage them before running workflow commands." >&2
    git -C "$ROOT_DIR" status --short --untracked-files=no >&2
    exit 1
  fi
}

status_cmd() {
  echo "[workflow] repo: $ROOT_DIR"
  echo "[workflow] branch: $(git -C "$ROOT_DIR" rev-parse --abbrev-ref HEAD)"
  echo "[workflow] head:   $(git -C "$ROOT_DIR" rev-parse --short HEAD)"
  echo "---"
  printf "%-16s %s\n" "${FORK_REMOTE}/${MAIN_BRANCH}" "$(git -C "$ROOT_DIR" rev-parse --short "${FORK_REMOTE}/${MAIN_BRANCH}" 2>/dev/null || echo 'missing')"
  printf "%-16s %s\n" "${UPSTREAM_REMOTE}/${MAIN_BRANCH}" "$(git -C "$ROOT_DIR" rev-parse --short "${UPSTREAM_REMOTE}/${MAIN_BRANCH}" 2>/dev/null || echo 'missing')"
  printf "%-16s %s\n" "${PROD_BRANCH}" "$(git -C "$ROOT_DIR" rev-parse --short "${PROD_BRANCH}" 2>/dev/null || echo 'missing')"
  echo "---"
  git -C "$ROOT_DIR" rev-list --left-right --count "${FORK_REMOTE}/${MAIN_BRANCH}...${UPSTREAM_REMOTE}/${MAIN_BRANCH}" 2>/dev/null | awk '{printf("origin/main vs upstream: left=%s right=%s\n", $1, $2)}'
  if git -C "$ROOT_DIR" rev-parse --verify "${PROD_BRANCH}^{commit}" >/dev/null 2>&1; then
    git -C "$ROOT_DIR" log --oneline --decorate -1 "$PROD_BRANCH"
  fi
}

sync_main_cmd() {
  require_cmd git
  ensure_clean_tracked_tree
  retry_fetch "$FORK_REMOTE"
  retry_fetch "$UPSTREAM_REMOTE"

  ensure_commitish "${UPSTREAM_REMOTE}/${MAIN_BRANCH}"
  ensure_commitish "${FORK_REMOTE}/${MAIN_BRANCH}"

  local backup="backup/pre-sync-main-$(date +%Y%m%d-%H%M%S)"
  git -C "$ROOT_DIR" branch "$backup" "$MAIN_BRANCH"
  echo "[workflow] backup branch: $backup"

  git -C "$ROOT_DIR" switch "$MAIN_BRANCH" >/dev/null
  git -C "$ROOT_DIR" merge --no-ff "${UPSTREAM_REMOTE}/${MAIN_BRANCH}"

  echo "[workflow] running core tests"
  go test ./internal/api/handlers/management -count=1
  go test ./internal/runtime/executor -count=1
  go test ./sdk/cliproxy/auth -count=1

  git -C "$ROOT_DIR" push "$FORK_REMOTE" "$MAIN_BRANCH"
  echo "[workflow] synced ${UPSTREAM_REMOTE}/${MAIN_BRANCH} into ${FORK_REMOTE}/${MAIN_BRANCH}"
}

promote_prod_cmd() {
  require_cmd git
  ensure_clean_tracked_tree
  local source_ref="${1:-$DEFAULT_SOURCE_REF}"
  ensure_commitish "$source_ref"

  git -C "$ROOT_DIR" branch -f "$PROD_BRANCH" "$source_ref"
  git -C "$ROOT_DIR" push -u "$FORK_REMOTE" "$PROD_BRANCH" --force-with-lease

  echo "[workflow] promoted ${source_ref} -> ${PROD_BRANCH}"
  git -C "$ROOT_DIR" log --oneline --decorate -1 "$PROD_BRANCH"
}

deploy_prod_cmd() {
  ensure_clean_tracked_tree
  local deploy_script
  deploy_script="$(resolve_deploy_script)"
  DEPLOY_REF="$PROD_BRANCH" "$deploy_script"
}

sync_promote_deploy_cmd() {
  local source_ref="${1:-$DEFAULT_SOURCE_REF}"
  sync_main_cmd
  promote_prod_cmd "$source_ref"
  deploy_prod_cmd
}

main() {
  require_repo
  local cmd="${1:-}"
  case "$cmd" in
    status)
      shift
      status_cmd "$@"
      ;;
    sync-main)
      shift
      sync_main_cmd "$@"
      ;;
    promote-prod)
      shift
      promote_prod_cmd "$@"
      ;;
    deploy-prod)
      shift
      deploy_prod_cmd "$@"
      ;;
    sync-promote-deploy)
      shift
      sync_promote_deploy_cmd "$@"
      ;;
    -h|--help|help|'')
      usage
      ;;
    *)
      echo "Unknown command: $cmd" >&2
      usage >&2
      exit 1
      ;;
  esac
}

main "$@"
