#!/usr/bin/env bash
# Per-package coverage report (RFC-0004 slice 4).
#
# Informational only: always exits 0, so it NEVER fails the build. The
# enforcing ratchet lives in scripts/coverage-gate.sh; this just shows
# where the uncovered code is.
#
# Aggregates `go tool cover -func` function rows by Go package directory
# (statement-weighted coverage from the profile), prints a Markdown table
# sorted by coverage ascending with the total row last, and bolds the
# bottom-5 package names. Appends to $GITHUB_STEP_SUMMARY when set, and
# always prints to stdout.
#
# Usage: scripts/coverage-report.sh [cover.out]
set -uo pipefail

profile=${1:-cover.out}
if [[ ! -f $profile ]]; then
  echo "coverage-report: no such profile: $profile" >&2
  exit 1
fi

if ! func_out=$(go tool cover -func="$profile"); then
  echo "coverage-report: go tool cover failed on $profile" >&2
  exit 1
fi

mod=""
if mod_out=$(go list -m 2>/dev/null); then
  mod=$mod_out
fi

# Directory of a profile file path, shortened against the module root
# (repo root files report as ".").
pkg_of_file() {
  local f=$1 d
  case $f in
    */*) d=${f%/*} ;;
    *) d=. ;;
  esac
  if [[ -n $mod ]]; then
    if [[ $d == "$mod" ]]; then
      d=.
    elif [[ $d == "$mod"/* ]]; then
      d=${d#"$mod"/}
    fi
  fi
  printf '%s' "$d"
}

declare -A total_stmts=()
declare -A hit_stmts=()
declare -A nfuncs=()
# Statement-weighted coverage per package, straight from the profile.
while IFS= read -r line; do
  case $line in
    mode:*) continue ;;
  esac
  loc=${line%% *}
  rest=${line#"$loc" }
  ns=${rest%% *}
  count=${rest#"$ns" }
  case $ns in
    *[!0-9]*) continue ;;
  esac
  case $count in
    *[!0-9]*) continue ;;
  esac
  file=${loc%%:*}
  pkg=$(pkg_of_file "$file")
  total_stmts[$pkg]=$(( ${total_stmts[$pkg]:-0} + ns ))
  if [[ $count != 0 ]]; then
    hit_stmts[$pkg]=$(( ${hit_stmts[$pkg]:-0} + ns ))
  fi
done < "$profile"

# Function counts per package, from go tool cover -func.
while IFS= read -r line; do
  case $line in
    total:*|"") continue ;;
  esac
  loc=${line%% *}
  file=${loc%%:*}
  pkg=$(pkg_of_file "$file")
  nfuncs[$pkg]=$(( ${nfuncs[$pkg]:-0} + 1 ))
done <<< "$func_out"

total_pct=$(awk '/^total:/ {print $NF}' <<< "$func_out")
total_pct=${total_pct%%%}
total_funcs=0

rows=""
for pkg in "${!total_stmts[@]}"; do
  t=${total_stmts[$pkg]}
  h=${hit_stmts[$pkg]:-0}
  f=${nfuncs[$pkg]:-0}
  total_funcs=$(( total_funcs + f ))
  if (( t == 0 )); then
    cov="0.0"
  else
    cov=$(awk -v h="$h" -v t="$t" 'BEGIN { printf "%.1f", 100 * h / t }')
  fi
  rows+="$cov	$pkg	$f
"
done

table="| package | funcs | coverage |
|---|---|---|"
rank=0
if [[ -n $rows ]]; then
  while IFS=$'\t' read -r cov pkg f; do
    rank=$(( rank + 1 ))
    if (( rank <= 5 )); then
      pkg="**$pkg**"
    fi
    table+="
| $pkg | $f | $cov% |"
  done < <(printf '%s' "$rows" | sort -t "$(printf '\t')" -k1,1n -k2,2)
fi
table+="
| **total** | $total_funcs | ${total_pct}% |"

printf '%s\n' "$table"
if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
  {
    echo '### Coverage by package'
    echo
    printf '%s\n' "$table"
  } >> "$GITHUB_STEP_SUMMARY"
fi
exit 0
