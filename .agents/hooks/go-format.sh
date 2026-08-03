#!/usr/bin/env bash
set -euo pipefail

python_bin="${AGENT_HARNESS_PYTHON:-python3}"
input=$(cat)

# `$python_bin` is invoked as a single word, so a multi-word value is not a
# usable interpreter here. Probe once and say so, rather than letting every
# later call fail silently and disable the hook with no diagnostic.
if ! "$python_bin" -c 'pass' >/dev/null 2>&1; then
  echo "[trello-cli-go-format] WARN: interpreter '$python_bin' is not runnable; hook skipped" >&2
  exit 0
fi

# A post-edit hook must never fail the edit it observes. Malformed hook JSON
# yields empty values and a clean exit rather than a non-zero status.
read_field() {
  printf '%s' "$input" | "$python_bin" -c "
import json, sys
try:
    data = json.load(sys.stdin)
except (json.JSONDecodeError, UnicodeDecodeError):
    sys.exit(0)
if not isinstance(data, dict):
    sys.exit(0)
if sys.argv[1] == 'file_path':
    section = data.get('tool_input', {})
    print(section.get('file_path', '') if isinstance(section, dict) else '')
else:
    print(data.get('cwd', ''))
" "$1" 2>/dev/null || true
}

fp=$(read_field file_path)
hook_cwd=$(read_field cwd)

if [[ -z "$fp" || "$fp" != *.go ]]; then
  exit 0
fi

if [[ "$fp" = /* ]]; then
  target=$fp
elif [[ -n "$hook_cwd" ]]; then
  target="${hook_cwd}/${fp}"
else
  target=$fp
fi

repo_root=$(git -C "${hook_cwd:-.}" rev-parse --show-toplevel 2>/dev/null || true)
if [[ -z "$repo_root" ]]; then
  echo "[trello-cli-go-format] WARN: cannot resolve repository root" >&2
  exit 0
fi

target=$(
  "$python_bin" - "$target" "$repo_root" <<'PY' || true
from pathlib import Path
import sys

target = Path(sys.argv[1]).resolve()
root = Path(sys.argv[2]).resolve()
try:
    target.relative_to(root)
except ValueError:
    sys.exit(0)
if target.is_file():
    print(target)
PY
)
if [[ -z "$target" ]]; then
  exit 0
fi

# Never reformat generated files. The convention is the `Code generated ... DO
# NOT EDIT.` header from https://pkg.go.dev/cmd/go#hdr-Generate_Go_files, which
# must appear before the package clause.
if "$python_bin" - "$target" <<'PY'
import re, sys
from pathlib import Path

pattern = re.compile(r"^// Code generated .* DO NOT EDIT\.$")
for line in Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace").splitlines():
    if line.startswith("package "):
        break
    if pattern.match(line):
        sys.exit(0)
sys.exit(1)
PY
then
  exit 0
fi

cd "$repo_root"
if ! command -v gofmt >/dev/null 2>&1; then
  echo "[trello-cli-go-format] WARN: gofmt not found; skipped formatting" >&2
  exit 0
fi
# Formatting is best-effort: a syntax error in the just-edited file must surface
# as a warning, not as a hook failure that masks the edit result.
if ! gofmt -w "$target" >/dev/null; then
  echo "[trello-cli-go-format] WARN: gofmt failed for $target" >&2
fi
exit 0
