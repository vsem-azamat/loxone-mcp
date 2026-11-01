#!/usr/bin/env bash
# Repository policy: AGENTS.md is the only agent instruction file, and nothing that identifies a
# real installation is committed (Miniserver serials, Remote Connect hosts, private overlays).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

fail=0
for f in CLAUDE.md .cursorrules .windsurfrules .clinerules .github/copilot-instructions.md; do
  if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then
    echo "repo-check: $f is not allowed; AGENTS.md is the only agent instruction file" >&2
    fail=1
  fi
done

# a Remote Connect relay host or a cloud DNS address with a real-looking serial (12 hex digits,
# not all zeros)
if git grep -nIiE '[0-9a-f]{12}\.dyndns\.loxonecloud\.com|loxonecloud\.com/[0-9a-f]{12}' -- . ':!scripts/repo-check.sh' \
  | grep -viE '0{12}|<serial>|<snr>'; then
  echo "repo-check: a real Miniserver address is committed" >&2
  fail=1
fi

# overlays other than the example
if git ls-files | grep -E '(^|/)home\.ya?ml$' | grep -v '^examples/'; then
  echo "repo-check: an overlay outside examples/ is committed" >&2
  fail=1
fi

exit $fail
