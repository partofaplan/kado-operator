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

tags=$(git tag --list 'v*' || true)

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
# 10# forces base 10: a tag like v1.08.0 would otherwise be read as octal and
# abort with "value too great for base".
case "$kind" in
  minor)   next="${rel}.${maj}.$((10#$min + 1))" ;;
  major)   next="${rel}.$((10#$maj + 1)).0" ;;
  release) next="$((10#$rel + 1)).0.0" ;;
esac

[[ "$next" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] \
  || { echo "::error::computed bad version '$next' from '$latest'" >&2; exit 1; }

if git rev-parse -q --verify "refs/tags/v${next}" >/dev/null; then
  echo "::error::tag v${next} already exists" >&2
  exit 1
fi

printf '%s\n' "$next"
