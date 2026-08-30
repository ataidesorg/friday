#!/usr/bin/env bash
# One bounded live turn against deepseek-v4-flash. Loads ~/.ink/env and
# never prints it. Not a go test: this hits the real provider.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
envf="${HOME}/.ink/env"
if [[ ! -f "$envf" ]]; then
  echo "skip: missing ~/.ink/env" >&2
  exit 0
fi
set -a
# shellcheck disable=SC1090
source "$envf"
set +a
bin="$root/bin/ink"
mkdir -p "$root/bin"
go build -o "$bin" "$root/cmd/ink"
proj="$(mktemp -d)"
trap 'rm -rf "$proj"' EXIT
git -C "$proj" init -q
git -C "$proj" config user.email "bench@localhost"
git -C "$proj" config user.name "ink-bench"
printf 'package p\nfunc Hi() string { return "hi" }\n' > "$proj/hi.go"
git -C "$proj" add hi.go
git -C "$proj" commit -q -m init
start="$(python3 -c 'import time; print(time.time())')"
set +e
out="$("$bin" run --project "$proj" --no-tui --yes "Reply with the single word pong and nothing else. Do not use tools." 2>&1)"
code=$?
set -e
elapsed="$(python3 -c "import time; print(round(time.time()-float('$start'), 3))")"
# Strip anything that looks like a credential before printing.
safe="$(printf '%s' "$out" | python3 -c 'import re,sys; t=sys.stdin.read(); t=re.sub(r"(sk-|fw_|ghp_|xai-)[A-Za-z0-9._-]{8,}","[redacted]",t); print(t)')"
printf '%s\n' "$safe"
echo "live-bench exit=$code elapsed=${elapsed}s"
if printf '%s' "$safe" | grep -qi 'pong'; then
  echo "live-bench MET (pong)"
  exit 0
fi
exit "$code"
