#!/usr/bin/env bash
# Coverage ratchet, the same shape as .ste-budget: a number seeded from the
# repo's own coverage at adoption, which only ever goes up.
#
# A gate that demands a round number nobody hit gets disabled the week it
# lands. A gate that says "not worse than yesterday" survives, and every PR
# that adds tests raises the bar for the next one.
#
# Usage: scripts/coverage-gate.sh [cover.out]
# Reads the floor from .coverage-budget. Writes the profile if not supplied.
set -euo pipefail

budget_file=.coverage-budget
profile=${1:-}

if [[ ! -f $budget_file ]]; then
  echo "no $budget_file — seed it with the current total:" >&2
  echo "  go test -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1" >&2
  exit 1
fi
budget=$(tr -d '[:space:]' < "$budget_file")

if [[ -z $profile ]]; then
  profile=cover.out
  go test -count=1 -timeout 300s -coverprofile="$profile" ./... >/dev/null
fi

total=$(go tool cover -func="$profile" | awk '/^total:/ {print $NF}' | tr -d '%')
echo "coverage: ${total}% (floor ${budget}%)"

if awk -v t="$total" -v b="$budget" 'BEGIN { exit !(t + 0.0001 < b) }'; then
  echo "::error::coverage fell below the floor: ${total}% < ${budget}%" >&2
  echo "Add tests, or say why the floor should move — never lower it to pass." >&2
  exit 1
fi

# A ratchet only works if someone tightens it. Say so, loudly, when there is
# slack: the number in the file is the promise, not the number in this run.
if awk -v t="$total" -v b="$budget" 'BEGIN { exit !(t >= b + 1) }'; then
  echo "::notice::coverage is ${total}%, a point or more above the ${budget}% floor — raise .coverage-budget"
fi
