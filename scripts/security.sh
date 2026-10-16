#!/bin/sh
# Preserve golangci-lint's native report and fail closed on incomplete scans.
set -eu
cd "$(dirname "$0")/.."
scripts/check-tools.sh go golangci-lint jq
config=.golangci-security.yml
if [ "${1:-}" = --hosted ]; then
    config=.golangci-security-hosted.yml
    shift
fi
report=${1:-security-report.json}
if [ "$#" -gt 0 ]; then shift; fi
if [ "$#" -eq 0 ]; then set -- ./...; fi
mkdir -p "$(dirname "$report")"
printf '%s\n' "$@" > "$report.scope"
printf '%s\n' "$config" > "$report.config"
rm -f "$report"
status=0
golangci-lint run -v --config "$config" --timeout=5m \
    --output.json.path "$report" "$@" \
    > "$report.stdout" 2> "$report.stderr" || status=$?
printf '%s\n' "$status" > "$report.exit"
cat "$report.stdout"
cat "$report.stderr" >&2

# Native golangci JSON, not standalone gosec's Stats/Issues schema. Typecheck is
# an implicit mandatory analyzer; no other linter should replace the gosec scan.
if ! jq -e '
    type == "object" and has("Issues") and
    (.Issues == null or (.Issues | type == "array")) and
    (.Report.Linters | type == "array") and
    ([.Report.Linters[] | select(.Enabled == true) | .Name] | sort == ["gosec", "typecheck"])
' "$report" >/dev/null; then
    printf 'Security report is missing, malformed, or from the wrong analysis.\n' >&2
    exit 1
fi
[ "$status" -eq 0 ] || exit "$status"
# Do not trust a producer that reports findings but unexpectedly returns zero.
jq -e '(.Issues // []) | length == 0' "$report" >/dev/null
