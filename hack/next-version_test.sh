#!/usr/bin/env bash
# Tests for next-version.sh, run against a throwaway git repo.
#
# Shell, not Go, because the thing under test is a shell script that CI calls
# directly. Getting this wrong mints an immutable tag and publishes an image
# under it, and neither can be moved afterwards.
set -uo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SCRIPT="$script_dir/next-version.sh"
fails=0

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$work"
git init -q .
git -c user.email=t@t -c user.name=t commit -q --allow-empty -m init

check() {
  local tags=() kind exp got
  while [ "$1" != "--" ]; do tags+=("$1"); shift; done; shift
  kind=$1; exp=$2
  # shellcheck disable=SC2046
  git tag -d $(git tag) >/dev/null 2>&1 || true
  for t in "${tags[@]}"; do [ -n "$t" ] && git tag "$t"; done
  got=$("$SCRIPT" "$kind" 2>/dev/null || echo "ERR")
  if [ "$got" = "$exp" ]; then
    printf '  ok   %-30s %-12s -> %s\n' "[${tags[*]}]" "$kind" "$got"
  else
    printf '  FAIL %-30s %-12s -> got %q want %q\n' "[${tags[*]}]" "$kind" "$got" "$exp"
    fails=$((fails + 1))
  fi
}

echo "the three bump kinds"
check v1.0.0             -- minor   1.0.1   # merge into develop
check v1.0.1             -- major   1.1.0   # merge into main
check v1.4.7             -- release 2.0.0   # release package
check v1.0.0             -- major   1.1.0
check v2.3.9             -- minor   2.3.10

echo "migration: the repo used two-place tags through v0.9"
check v0.7 v0.8 v0.9     -- minor   0.9.1
check v0.7 v0.8 v0.9     -- major   0.10.0
check v0.7 v0.8 v0.9     -- release 1.0.0
# Once a three-place tag exists the legacy seed must never be consulted again,
# or numbering would jump backwards.
check v0.9 v0.9.1        -- minor   0.9.2
check v0.9 v0.9.1        -- major   0.10.0

echo "version ordering, not lexical"
check v0.9.9 v0.10.0     -- minor   0.10.1
check v1.9.0 v1.10.0     -- major   1.11.0
check v1.0.0 v2.0.0 v10.0.0 -- release 11.0.0

echo "a zero-padded component must not be read as octal"
check v1.08.0            -- minor   1.08.1

echo "no tags at all"
check ""                 -- minor   0.0.1
check ""                 -- release 1.0.0

echo "last-release finds vR.0.0 only"
check v0.9 v1.0.0 v1.2.3 v2.0.0 -- last-release v2.0.0
check v0.9 v0.9.1               -- last-release ""
check v1.0.0 v1.2.0 v1.0.3      -- last-release v1.0.0

echo "refuses to restart numbering from malformed tags"
check vfoo v1.2-rc1      -- minor   ERR
check vfoo v1.2-rc1      -- release ERR

echo "rejects an unknown bump kind"
if "$SCRIPT" sideways >/dev/null 2>&1; then
  printf '  FAIL unknown kind was accepted\n'; fails=$((fails + 1))
else
  printf '  ok   unknown kind rejected\n'
fi

echo
if [ "$fails" -eq 0 ]; then echo "PASS"; else echo "FAIL ($fails)"; exit 1; fi
