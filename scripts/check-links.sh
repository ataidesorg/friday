#!/bin/sh
# Fails if a relative markdown link (outside .git/.superpowers/bin) is missing.
# Skips #fragments, bare #anchors, http(s):, and mailto: targets.
set -u
cd "$(dirname "$0")/.."
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

find . \( -path ./.git -o -path ./.superpowers -o -path ./bin \) -prune -o -name '*.md' -print |
while read -r file; do
  dir=$(dirname "$file")
  grep -oE '\]\([^)]*\)' "$file" | sed -E 's/^\]\(//; s/\)$//' |
  while read -r target; do
    target=${target%%#*}
    [ -z "$target" ] && continue
    [ "${target#http://}" != "$target" ] && continue
    [ "${target#https://}" != "$target" ] && continue
    [ "${target#mailto:}" != "$target" ] && continue
    [ -e "$dir/$target" ] || printf '%s: broken link -> %s\n' "$file" "$target" >>"$tmp"
  done
done

[ -s "$tmp" ] || exit 0
cat "$tmp" >&2
exit 1
