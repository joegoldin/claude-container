#!/usr/bin/env bash
# Run the claude-container security E2E suite outside the Claude Code
# sandbox so docker is actually reachable.
#
# Streams live progress (with colors + timestamps) to your terminal AND
# writes a plain-text log + machine-readable JSON to ./tmp/security-tests/
# so you can paste them back to Claude for review.
#
# Usage:
#   ./scripts/run-security-tests.sh                 # OS-level probes only (default)
#   ./scripts/run-security-tests.sh --llm           # include LLM-driven probes (costs tokens)
#   ./scripts/run-security-tests.sh --pattern X     # restrict to tests matching name pattern X

set -uo pipefail

# ---------------------------------------------------------------------------
# Signal handling — single Ctrl+C aborts the whole script (not just the
# currently-running `go test`) and best-effort-removes any sec-* containers
# left running so the next invocation starts clean.
# ---------------------------------------------------------------------------

ABORTED=0
cleanup_security_containers() {
  docker ps -aq --filter "name=^claude-container_sec-" 2>/dev/null | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker ps -aq --filter "name=^claude-proxy_sec-"     2>/dev/null | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network ls -q --filter "name=^claude-proxy-net_sec-" 2>/dev/null | xargs -r docker network rm >/dev/null 2>&1 || true
}
on_abort() {
  ABORTED=1
  printf '\n\033[31m[abort]\033[0m Ctrl+C — stopping and cleaning up sec-* containers...\n'
  cleanup_security_containers
  exit 130
}
trap on_abort INT TERM

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

OUT_DIR="$REPO_ROOT/tmp/security-tests"
mkdir -p "$OUT_DIR"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
LOG_FILE="$OUT_DIR/run-$STAMP.log"
JSON_FILE="$OUT_DIR/run-$STAMP.json"
SUMMARY_FILE="$OUT_DIR/run-$STAMP.summary.txt"

PATTERN="^TestSecurity_"          # OS-level probes by default
INCLUDE_LLM=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --llm)
      INCLUDE_LLM=1
      PATTERN="^TestSecurity"     # both groups
      export CLAUDE_CONTAINER_SECURITY_LLM_TESTS=1
      shift
      ;;
    --pattern)
      PATTERN="$2"
      shift 2
      ;;
    -h|--help)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 2
      ;;
  esac
done

# ---------------------------------------------------------------------------
# Color + logging helpers
# ---------------------------------------------------------------------------

if [[ -t 1 ]]; then
  C_RESET=$'\033[0m'
  C_DIM=$'\033[2m'
  C_RED=$'\033[31m'
  C_GREEN=$'\033[32m'
  C_YELLOW=$'\033[33m'
  C_BLUE=$'\033[34m'
  C_BOLD=$'\033[1m'
else
  C_RESET="" C_DIM="" C_RED="" C_GREEN="" C_YELLOW="" C_BLUE="" C_BOLD=""
fi

now_ms() { date +%s%3N; }
ts() { date -u +%H:%M:%S; }

info()  { printf '%s[%s]%s %s\n'      "$C_BLUE"   "$(ts)" "$C_RESET" "$*"; }
ok()    { printf '%s[%s]%s %s%s%s\n'  "$C_GREEN"  "$(ts)" "$C_RESET" "$C_GREEN"  "$*" "$C_RESET"; }
warn()  { printf '%s[%s]%s %s%s%s\n'  "$C_YELLOW" "$(ts)" "$C_RESET" "$C_YELLOW" "$*" "$C_RESET"; }
err()   { printf '%s[%s]%s %s%s%s\n'  "$C_RED"    "$(ts)" "$C_RESET" "$C_RED"    "$*" "$C_RESET"; }

# Strip ANSI codes when writing to the log so my (Claude's) reader sees clean text.
strip_ansi() { sed -E 's/\x1b\[[0-9;]*[mGKHF]//g'; }

# Tee everything from this point on (stdout + stderr) into the log, with ANSI
# stripped on the log side but preserved for the terminal.
exec > >(tee >(strip_ansi >> "$LOG_FILE")) 2>&1

# ---------------------------------------------------------------------------
# Banner + preflight
# ---------------------------------------------------------------------------

printf '%s================================================================%s\n' "$C_BOLD" "$C_RESET"
printf '%sclaude-container security E2E suite%s\n' "$C_BOLD" "$C_RESET"
printf '  pattern:    %s\n' "$PATTERN"
printf '  llm probes: %s\n' "$( [[ $INCLUDE_LLM == 1 ]] && echo "enabled" || echo "disabled (use --llm to enable)" )"
printf '  log file:   %s\n' "$LOG_FILE"
printf '  json file:  %s\n' "$JSON_FILE"
printf '  started:    %s\n' "$(date -u +%FT%TZ)"
printf '%s================================================================%s\n' "$C_BOLD" "$C_RESET"
echo

info "preflight: docker daemon"
if ! docker info >/dev/null 2>&1; then
  err "docker daemon not reachable. abort."
  exit 1
fi
ok "docker daemon reachable"

info "preflight: claude-code image (loaded as 'claude-code', containers are claude-container_<session>)"
if [[ -z "$(docker images -q claude-code 2>/dev/null)" ]]; then
  warn "image 'claude-code' not loaded — tests that need it will fail or skip"
else
  ok "image present"
fi

info "preflight: claude-proxy image"
if [[ -z "$(docker images -q claude-proxy 2>/dev/null)" ]]; then
  warn "image 'claude-proxy' not loaded — tests that need it will fail or skip"
else
  ok "proxy image present"
fi

if [[ "$INCLUDE_LLM" == 1 ]]; then
  info "preflight: claude credentials (for LLM probes)"
  if [[ ! -f "$HOME/.claude/.credentials.json" ]]; then
    warn "no ~/.claude/.credentials.json — LLM probes will skip"
  else
    ok "credentials present"
  fi
fi

echo

# ---------------------------------------------------------------------------
# Enumerate tests in the target pattern
# ---------------------------------------------------------------------------

info "enumerating tests matching /$PATTERN/"
TESTS=$(devenv shell -- go test ./cmd/ -list "$PATTERN" 2>/dev/null | grep -E '^TestSecurity' || true)

if [[ -z "$TESTS" ]]; then
  err "no tests matched pattern $PATTERN"
  exit 1
fi

N_TESTS=$(echo "$TESTS" | wc -l | tr -d ' ')
ok "found $N_TESTS test(s)"
echo "$TESTS" | sed "s/^/  ${C_DIM}- ${C_RESET}/"
echo

# ---------------------------------------------------------------------------
# Run each test individually with per-test timing
# ---------------------------------------------------------------------------

declare -i N_PASS=0 N_FAIL=0 N_SKIP=0
TOTAL_START=$(now_ms)

FAILED_TESTS=()
SLOW_TESTS=()   # entries like "12345ms TestName"

i=0
while IFS= read -r TEST_NAME; do
  [[ -z "$TEST_NAME" ]] && continue
  i=$((i + 1))

  printf '%s[%d/%d]%s %s%s%s …\n' "$C_BLUE" "$i" "$N_TESTS" "$C_RESET" "$C_BOLD" "$TEST_NAME" "$C_RESET"

  T_START=$(now_ms)
  # Stream test output live to the terminal (dimmed so it stands apart
  # from our own log lines) and capture it for the PASS/FAIL/SKIP parse.
  TMP_OUT=$(mktemp)
  devenv shell -- go test ./cmd/ -run "^${TEST_NAME}\$" -v -timeout 180s -count=1 2>&1 \
    | tee "$TMP_OUT" \
    | sed "s/^/    ${C_DIM}│${C_RESET} /"
  RC=${PIPESTATUS[0]}
  OUT=$(cat "$TMP_OUT")
  rm -f "$TMP_OUT"
  T_END=$(now_ms)
  T_MS=$(( T_END - T_START ))

  SLOW_TESTS+=("${T_MS}ms ${TEST_NAME}")

  if echo "$OUT" | grep -q -- '--- SKIP'; then
    N_SKIP=$((N_SKIP + 1))
    printf '  %s→ SKIP%s %sms\n' "$C_YELLOW" "$C_RESET" "$T_MS"
  elif [[ $RC -eq 0 ]] && echo "$OUT" | grep -q -- '--- PASS'; then
    N_PASS=$((N_PASS + 1))
    printf '  %s→ PASS%s %sms\n' "$C_GREEN" "$C_RESET" "$T_MS"
  else
    N_FAIL=$((N_FAIL + 1))
    FAILED_TESTS+=("$TEST_NAME")
    printf '  %s→ FAIL%s %sms (exit %s)\n' "$C_RED" "$C_RESET" "$T_MS" "$RC"
  fi
done <<< "$TESTS"

TOTAL_MS=$(( $(now_ms) - TOTAL_START ))

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo
printf '%s================================================================%s\n' "$C_BOLD" "$C_RESET"
printf '%sSUMMARY%s\n' "$C_BOLD" "$C_RESET"
printf '  passed:  %s%d%s\n' "$C_GREEN"  "$N_PASS" "$C_RESET"
printf '  failed:  %s%d%s\n' "$C_RED"    "$N_FAIL" "$C_RESET"
printf '  skipped: %s%d%s\n' "$C_YELLOW" "$N_SKIP" "$C_RESET"
printf '  total:   %d test(s) in %dms (%.1fs)\n' "$N_TESTS" "$TOTAL_MS" "$(echo "$TOTAL_MS / 1000" | bc -l)"
printf '%s================================================================%s\n' "$C_BOLD" "$C_RESET"

if [[ ${#FAILED_TESTS[@]} -gt 0 ]]; then
  echo
  printf '%sFailures:%s\n' "$C_RED" "$C_RESET"
  for t in "${FAILED_TESTS[@]}"; do
    echo "  - $t"
  done
fi

echo
echo "Slowest tests:"
printf '%s\n' "${SLOW_TESTS[@]}" | sort -rn | head -5 | sed 's/^/  /'

# Also dump the structured summary to a file for easy paste-back.
{
  echo "claude-container security E2E run — $(date -u +%FT%TZ)"
  echo "pattern: $PATTERN"
  echo "llm probes: $( [[ $INCLUDE_LLM == 1 ]] && echo enabled || echo disabled )"
  echo
  echo "passed:  $N_PASS"
  echo "failed:  $N_FAIL"
  echo "skipped: $N_SKIP"
  echo "total:   $N_TESTS"
  echo "duration_ms: $TOTAL_MS"
  echo
  if [[ ${#FAILED_TESTS[@]} -gt 0 ]]; then
    echo "Failures:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    echo
  fi
  echo "Slowest tests:"
  printf '%s\n' "${SLOW_TESTS[@]}" | sort -rn | head -5 | sed 's/^/  /'
} | tee "$SUMMARY_FILE" >/dev/null

# Write JSON for tooling.
{
  echo "{"
  echo "  \"started\":  \"$(date -u +%FT%TZ)\","
  echo "  \"pattern\":  \"$PATTERN\","
  echo "  \"llm\":      $( [[ $INCLUDE_LLM == 1 ]] && echo true || echo false ),"
  echo "  \"passed\":   $N_PASS,"
  echo "  \"failed\":   $N_FAIL,"
  echo "  \"skipped\":  $N_SKIP,"
  echo "  \"total\":    $N_TESTS,"
  echo "  \"duration_ms\": $TOTAL_MS,"
  echo -n "  \"failures\": ["
  for i in "${!FAILED_TESTS[@]}"; do
    [[ $i -gt 0 ]] && echo -n ","
    echo -n "\"${FAILED_TESTS[$i]}\""
  done
  echo "]"
  echo "}"
} > "$JSON_FILE"

echo
info "outputs:"
echo "  log:     $LOG_FILE"
echo "  summary: $SUMMARY_FILE"
echo "  json:    $JSON_FILE"

# Exit non-zero on any failure so CI / shell chains can detect it.
[[ $N_FAIL -eq 0 ]]
