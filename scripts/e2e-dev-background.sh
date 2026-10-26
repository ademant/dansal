#!/usr/bin/env bash
# Run the Playwright e2e suite against dev in the background.
#
#   scripts/e2e-dev-background.sh [--shuffle[=SEED]] [--repeat=N] [spec files...] [playwright args...]
#
# Each run writes its output to e2e/runs/<timestamp>.log. e2e/full-run.log links
# to the latest one, and each finished run appends a line to e2e/runs/index.jsonl
# (start/finish time, arguments, exit code, pass/fail/flaky/skipped counts,
# failing tests). The PID of the active run goes to $PIDFILE (default
# e2e/full-run.pid). Stop it with: kill "$(cat e2e/full-run.pid)"
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
# Spec files given as arguments are the set to run; without any, the whole
# suite is used. Other arguments are passed to `npx playwright test`, e.g.
#   scripts/e2e-dev-background.sh --shuffle=42 --repeat=3 --project=desktop
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
runs="$root/e2e/runs"
index="$runs/index.jsonl"

# Appends one summary line for the run in $E2E_RUN_LOG to $index.
# Args: exit code.
record_run() {
  python3 - "$E2E_RUN_LOG" "$index" "$1" "$E2E_RUN_ARGS" "$E2E_RUN_STARTED" <<'PY'
import datetime, json, re, sys

log, index, exit_code, args, started = sys.argv[1:6]
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

record = {
    "started": started,
    "finished": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
    "args": args,
    "exit": int(exit_code),
    "log": log,
    **counts,
    "failed_tests": failed,
}
with open(index, "a") as f:
    f.write(json.dumps(record) + "\n")
PY
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
  files=()
  opts=()
  for arg in "$@"; do
    case "$arg" in
      --shuffle) shuffle="$(date +%s)" ;;
      --shuffle=*) shuffle="${arg#--shuffle=}" ;;
      --repeat=*) repeat="${arg#--repeat=}" ;;
      -*) opts+=("$arg") ;;
      *) files+=("$arg") ;;
    esac
  done
  if ! [[ "$repeat" =~ ^[1-9][0-9]*$ ]]; then
    echo "--repeat must be a positive integer, got: $repeat" >&2
    exit 2
  fi

  if [[ ${#files[@]} -eq 0 ]]; then
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

  # No --reporter flag: the config's reporters (list, json, metrics, failure
  # summary) run as written. Passing --reporter would replace them all.
  overall=0
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
    for f in "${order[@]}"; do
      echo "== $f"
      npx playwright test "$f" --workers=1 "${opts[@]}" && rc=0 || rc=$?
      if [[ $rc -ne 0 ]]; then round_rc=$rc; fi
    done
    echo "=== round $round/$repeat exit $round_rc"
    if [[ $round_rc -ne 0 ]]; then overall=$round_rc; fi
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
