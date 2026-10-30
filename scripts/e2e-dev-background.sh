#!/usr/bin/env bash
# Run the Playwright e2e suite against dev in the background.
#
#   scripts/e2e-dev-background.sh [--shuffle[=SEED]] [--repeat=N] [--smoke] [spec files...] [playwright args...]
#
# Each run writes its output to e2e/runs/<timestamp>.log. e2e/full-run.log links
# to the latest one, and each finished run appends a line to e2e/runs/index.jsonl
# (start/finish time, arguments, exit code, pass/fail/flaky/skipped counts,
# failing tests with their cause and API error_id, and the tests that are new
# or fixed compared with the previous run). The PID of the active run goes to
# $PIDFILE (default e2e/full-run.pid). Stop it with:
#   kill "$(cat e2e/full-run.pid)"
#
# Causes (printed per failure and stored in index.jsonl):
#   infrastructure  the admin CLI, API or admin socket failed, not the page
#   timeout         a test or wait ran out of time
#   strict-mode     a locator matched several elements (a test bug)
#   assertion       the page or API did not match the expectation
#
# Health: before each spec file the API (/api/v1/health) and the admin socket
# are checked, retried three times 10 s apart. A file whose check fails is
# skipped and recorded as an infrastructure failure. A file that produces three
# infrastructure failures in a row ends the round; a run with three failed
# health checks in a row stops at once. Either way the exit code is 3.
#
# --shuffle runs each spec file in its own `playwright test` call, in a random
# order drawn from SEED (default: the current time; printed at the top of the
# log so an order can be replayed). Playwright itself always runs files in
# alphabetical order and cannot shuffle, so this is the only way to randomise
# across files. Order within a file is unchanged.
#
# --repeat=N runs the whole set N times in a row. With --shuffle, round i uses
# seed SEED+i, so each round has its own order and the set can still be replayed.
#
# --smoke runs a quick check of the dev instance: the @smoke-tagged tests of
# every spec file, plus the public-pages sweep (public-pages-js-errors.spec.ts)
# in full. Use it after a deploy to dev. It ignores explicit spec files.
#
# Spec files given as arguments are the set to run; without any, the whole
# suite is used. Other arguments are passed to `npx playwright test`, e.g.
#   scripts/e2e-dev-background.sh --shuffle=42 --repeat=3 --project=desktop
#   scripts/e2e-dev-background.sh --smoke --project=desktop
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
runs="$root/e2e/runs"
index="$runs/index.jsonl"
failures="$root/e2e/test-results/failures.jsonl"

# Appends one summary line for the run in $E2E_RUN_LOG to $index, then prints
# the tests that are new or fixed compared with the previous record (into the
# log). Args: exit code.
record_run() {
  python3 - "$E2E_RUN_LOG" "$index" "$1" "$E2E_RUN_ARGS" "$E2E_RUN_STARTED" "$failures" <<'PY'
import datetime, json, os, re, sys

log, index, exit_code, args, started, failures_path = sys.argv[1:7]
counts = {"passed": 0, "failed": 0, "flaky": 0, "skipped": 0}
failed = []
with open(log, errors="replace") as f:
    for line in f:
        m = re.match(r"^  (\d+) (passed|failed|flaky|skipped)\b", line)
        if m:
            counts[m.group(2)] += int(m.group(1))
        m = re.match(r"^  \d+\) (.+?)\s*$", line)
        if m:
            failed.append(m.group(1))

# One entry per failed test, from the reporter's sidecar (empty if none ran).
details = []
if os.path.exists(failures_path):
    with open(failures_path) as f:
        details = [json.loads(l) for l in f if l.strip()]

def key(d):
    return f'{d["project"]} {d["title"]}'

previous = None
if os.path.exists(index):
    with open(index) as f:
        for l in f:
            if l.strip():
                previous = json.loads(l)

now_failing = {key(d) for d in details}
if previous is None or "failures" not in previous:
    print("\nrun diff: no previous run with failure details to compare with")
else:
    before = {key(d) for d in previous["failures"]}
    new = sorted(now_failing - before)
    fixed = sorted(before - now_failing)
    print(f"\nrun diff vs {previous['finished']} (exit {previous['exit']}):")
    print(f"  new failures: {len(new)}" + "".join(f"\n    + {k}" for k in new))
    print(f"  fixed since then: {len(fixed)}" + "".join(f"\n    - {k}" for k in fixed))
    if not new and not fixed:
        print("  no change in the set of failing tests")

causes = {}
for d in details:
    causes[d["cause"]] = causes.get(d["cause"], 0) + 1
if causes:
    print("causes: " + ", ".join(f"{k} {v}" for k, v in sorted(causes.items())))

record = {
    "started": started,
    "finished": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
    "args": args,
    "exit": int(exit_code),
    "log": log,
    **counts,
    "failed_tests": failed,
    "causes": causes,
    "failures": details,
}
with open(index, "a") as f:
    f.write(json.dumps(record) + "\n")
PY
}

# True when the API answers its health endpoint and the admin socket exists.
# Args: none. Uses $API_URL and $ADMIN_SOCKET from the worker environment.
healthy() {
  curl -fsS -m 5 "$API_URL/api/v1/health" >/dev/null 2>&1 && [[ -S "$ADMIN_SOCKET" ]]
}

# Retries healthy() three times, 10 s apart. Returns 1 if it never passes.
wait_healthy() {
  local attempt
  for attempt in 1 2 3; do
    if healthy; then return 0; fi
    echo "health check failed (attempt $attempt/3): API $API_URL or socket $ADMIN_SOCKET"
    [[ $attempt -lt 3 ]] && sleep 10
  done
  return 1
}

# Number of infrastructure-cause failures recorded so far in this run.
infra_count() {
  # The reporter writes compact JSON (no space after the colon).
  if [[ -f "$failures" ]]; then grep -c '"cause":"infrastructure"' "$failures" || true; else echo 0; fi
}

if [[ "${1:-}" == "--worker" ]]; then
  shift
  cd "$root/e2e"

  export BASE_URL=https://dev.balfolk.jetzt
  export API_URL=http://127.0.0.1:8000
  export ADMIN_CLI=/usr/lib/dansal/dev/dansal_admin
  export ADMIN_SOCKET=/var/lib/dansal/dev/dansal.sock
  export DANSAL_MAIL_FILE=/var/lib/dansal-e2e-mail/dansal-e2e-mail.mbox

  shuffle=""
  repeat=1
  smoke=0
  files=()
  opts=()
  for arg in "$@"; do
    case "$arg" in
      --shuffle) shuffle="$(date +%s)" ;;
      --shuffle=*) shuffle="${arg#--shuffle=}" ;;
      --repeat=*) repeat="${arg#--repeat=}" ;;
      --smoke) smoke=1 ;;
      -*) opts+=("$arg") ;;
      *) files+=("$arg") ;;
    esac
  done
  if ! [[ "$repeat" =~ ^[1-9][0-9]*$ ]]; then
    echo "--repeat must be a positive integer, got: $repeat" >&2
    exit 2
  fi

  mkdir -p test-results
  : > "$failures"

  if [[ $smoke -eq 1 ]]; then
    # Every spec with a @smoke test, plus the public-pages sweep (run in full).
    mapfile -t files < <(grep -rl '@smoke' tests --include='*.spec.ts' | sort)
    files+=(tests/journeys/public-pages-js-errors.spec.ts)
    mapfile -t files < <(printf '%s\n' "${files[@]}" | sort -u)
  elif [[ ${#files[@]} -eq 0 ]]; then
    mapfile -t files < <(find tests -name '*.spec.ts' | sort)
  fi

  # Order for one round. Seeded from $1 so a round can be replayed.
  order_for() {
    printf '%s\n' "${files[@]}" | python3 -c '
import random, sys
files = [l.rstrip("\n") for l in sys.stdin if l.strip()]
random.Random(sys.argv[1]).shuffle(files)
print("\n".join(files))
' "$1"
  }

  # Extra playwright args for one file. In smoke mode a spec runs only its
  # @smoke tests, except the public-pages sweep, which runs in full.
  file_opts() {
    if [[ $smoke -eq 1 && "$1" != *public-pages-js-errors* ]]; then
      echo "--grep @smoke"
    fi
  }

  # No --reporter flag: the config's reporters (list, json, metrics, failure
  # summary) run as written. Passing --reporter would replace them all.
  overall=0
  consecutive_down=0
  for ((round = 1; round <= repeat; round++)); do
    if [[ -n "$shuffle" ]]; then
      seed=$((shuffle + round - 1))
      mapfile -t order < <(order_for "$seed")
      echo "=== round $round/$repeat, shuffle seed $seed (${#order[@]} files)"
      printf '  %s\n' "${order[@]}"
    else
      order=("${files[@]}")
      echo "=== round $round/$repeat (${#order[@]} files, alphabetical)"
    fi

    round_rc=0
    consecutive_infra=0
    for f in "${order[@]}"; do
      if ! wait_healthy; then
        consecutive_down=$((consecutive_down + 1))
        echo "== SKIP $f: API or admin socket down (${consecutive_down} in a row)"
        if [[ $consecutive_down -ge 3 ]]; then
          echo "ABORT: API or admin socket down for 3 health checks in a row"
          echo "EXIT 3"
          record_run 3
          exit 3
        fi
        round_rc=3
        continue
      fi
      consecutive_down=0

      echo "== $f"
      before=$(infra_count)
      # shellcheck disable=SC2046
      npx playwright test "$f" --workers=1 $(file_opts "$f") "${opts[@]}" && rc=0 || rc=$?
      if [[ $rc -ne 0 ]]; then round_rc=$rc; fi

      # Three files in a row that failed on infrastructure mean the instance is
      # not usable: stop the round rather than run the rest into the same wall.
      if [[ $(infra_count) -gt $before ]]; then
        consecutive_infra=$((consecutive_infra + 1))
      else
        consecutive_infra=0
      fi
      if [[ $consecutive_infra -ge 3 ]]; then
        echo "ABORT: 3 files in a row failed on infrastructure (admin CLI, API or socket)"
        round_rc=3
        break
      fi
    done
    echo "=== round $round/$repeat exit $round_rc"
    if [[ $round_rc -ne 0 ]]; then overall=$round_rc; fi
    if [[ $consecutive_infra -ge 3 ]]; then break; fi
  done
  echo "EXIT $overall"
  record_run "$overall"
  exit "$overall"
fi

PIDFILE="${PIDFILE:-$root/e2e/full-run.pid}"
if [[ -f "$PIDFILE" ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
  echo "already running as PID $(cat "$PIDFILE")" >&2
  exit 1
fi

mkdir -p "$runs"
stamp="$(date +%Y%m%d-%H%M%S)"
LOG="${LOG:-$runs/$stamp.log}"
ln -sfn "$LOG" "$root/e2e/full-run.log"

export E2E_RUN_LOG="$LOG"
export E2E_RUN_ARGS="$*"
export E2E_RUN_STARTED="$(date --iso-8601=seconds)"

nohup "$0" --worker "$@" > "$LOG" 2>&1 &

echo $! > "$PIDFILE"
echo "started PID $(cat "$PIDFILE"), log: $LOG"
