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

# The component being INCREMENTED must not be read as octal, and the ones
# passed through must not keep their leading zero — SemVer2 forbids it and
# `helm package --version 1.08.1` would be rejected. The original test here
# only covered v1.08.0/minor, where the incremented component is 0 and every
# guard is a no-op, so it killed no mutant.
echo "zero-padded components are normalised, never read as octal"
check v1.08.0            -- minor   1.8.1
check v1.08.0            -- major   1.9.0
check v1.0.08            -- minor   1.0.9
check v09.0.0            -- release 10.0.0
check v1.09.9            -- minor   1.9.10

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

# TWO GUARDS ARE DELIBERATELY UNTESTED, because neither can be reached while
# the logic above them is correct. Both are defence-in-depth against a future
# change, and a test that appeared to exercise either would be testing nothing.
# Recorded here rather than left as untested lines someone later assumes are
# covered:
#
#   1. "tag v$next already exists" — every path takes the highest matching tag
#      and adds one, so the result is always greater than anything present.
#      Normalising a zero-padded component opens no gap either: sort -V already
#      ranks v1.08.0 below v1.8.1.
#   2. the final ^[0-9]+\.[0-9]+\.[0-9]+$ check on the computed version — the
#      arithmetic cannot produce anything else once every component has been
#      through 10#. Verified by mutation: weakening this check alone changes no
#      observable behaviour.

echo "refuses to run outside a git repository"
if (cd /tmp && "$SCRIPT" minor >/dev/null 2>&1); then
  printf '  FAIL ran outside a repo and invented a version\n'; fails=$((fails + 1))
else
  printf '  ok   non-repo refused\n'
fi

echo "rejects an unknown bump kind"
if "$SCRIPT" sideways >/dev/null 2>&1; then
  printf '  FAIL unknown kind was accepted\n'; fails=$((fails + 1))
else
  printf '  ok   unknown kind rejected\n'
fi

echo
if [ "$fails" -eq 0 ]; then echo "PASS"; else echo "FAIL ($fails)"; exit 1; fi
