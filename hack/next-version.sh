#!/usr/bin/env bash
# Computes the next version under the RELEASE.MAJOR.MINOR scheme, from git tags.
#
#   minor    merge into develop      R.M.m -> R.M.(m+1)
#   major    merge into main         R.M.m -> R.(M+1).0
#   release  a release package       R.M.m -> (R+1).0.0
#
# Also:
#   last-release   prints the newest release tag (vR.0.0), or nothing if none
#
# Tags are the source of truth, so there is no VERSION file to drift. Lives in
# one script rather than inline in each workflow: ci.yml and release.yml both
# need this arithmetic, and two copies would eventually disagree about what
# "next" means.
set -euo pipefail

kind=${1:-}
case "$kind" in
  minor|major|release|last-release) ;;
  *) echo "usage: $0 <minor|major|release|last-release>" >&2; exit 2 ;;
esac

# No `|| true` on the git call. Swallowing a git failure — run outside a repo,
# or in a clone whose tags were not fetched — would look exactly like "no tags
# exist", restart numbering at 0.0.1 and move every version backwards. That is
# the outcome the malformed-tag guard below exists to prevent, reached by a
# different door.
git rev-parse --git-dir >/dev/null 2>&1 \
  || { echo "::error::not a git repository; refusing to guess a version" >&2; exit 1; }
tags=$(git tag --list 'v*')

# A release tag is one where both MAJOR and MINOR are zero. That is what makes
# "since the last release" answerable without a separate marker.
last_release=$(printf '%s\n' "$tags" | grep -E '^v[0-9]+\.0\.0$' | sort -V | tail -n1 || true)
if [ "$kind" = "last-release" ]; then
  printf '%s\n' "$last_release"
  exit 0
fi

three=$(printf '%s\n' "$tags" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n1 || true)
two=$(printf '%s\n' "$tags"   | grep -E '^v[0-9]+\.[0-9]+$'          | sort -V | tail -n1 || true)
any=$(printf '%s\n' "$tags"   | grep -cE '^v' || true)

if [ -n "$three" ]; then
  latest=${three#v}
elif [ -n "$two" ]; then
  # Migration seed. The repo used a two-place vMAJOR.MINOR scheme through v0.9;
  # treating the highest of those as R.M.0 keeps numbering monotonic, so no new
  # tag ever sorts below an image already on Docker Hub. Once a three-place tag
  # exists this branch is dead, and it can be deleted.
  latest="${two#v}.0"
  echo "note: seeding from legacy two-place tag ${two} as ${latest}" >&2
elif [ "${any:-0}" -gt 0 ]; then
  # Numbering would otherwise silently restart at 0.0.1 and move backwards.
  echo "::error::v* tags exist but none match vR.M.m or legacy vM.m; refusing to restart numbering" >&2
  exit 1
else
  latest="0.0.0"
fi

IFS=. read -r rel maj min <<<"$latest"

# Every component goes through 10#, not just the one being incremented. Two
# reasons: a tag like v1.08.0 would otherwise be read as octal and abort with
# "value too great for base"; and passing an un-incremented component through
# verbatim would propagate its leading zero into the new version, producing
# something like 1.08.1 — which Helm rejects, because SemVer2 forbids a leading
# zero in a numeric identifier, and `helm package --version` would fail at the
# worst possible moment.
rel=$((10#$rel)); maj=$((10#$maj)); min=$((10#$min))

case "$kind" in
  minor)   next="${rel}.${maj}.$((min + 1))" ;;
  major)   next="${rel}.$((maj + 1)).0" ;;
  release) next="$((rel + 1)).0.0" ;;
esac

[[ "$next" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] \
  || { echo "::error::computed bad version '$next' from '$latest'" >&2; exit 1; }

if git rev-parse -q --verify "refs/tags/v${next}" >/dev/null; then
  echo "::error::tag v${next} already exists" >&2
  exit 1
fi

printf '%s\n' "$next"
