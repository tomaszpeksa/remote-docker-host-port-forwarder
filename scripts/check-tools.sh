#!/bin/sh
# Check required providers without installing tools or executing ambient copies.
set -eu
cd "$(dirname "$0")/.."

fail() {
    printf 'required project tool: %s\n' "$*" >&2
    printf 'Install the named locked tool set, then run through mise exec (see CONTRIBUTING.md).\n' >&2
    exit 1
}

command -v mise >/dev/null 2>&1 || fail 'mise is not on PATH'
[ "$#" -gt 0 ] || fail 'name at least one tool to check'
for binary in "$@"; do
    case "$binary" in
        go|gofmt) tool=core:go ;;
        golangci-lint) tool=aqua:golangci/golangci-lint ;;
        govulncheck) tool=go:golang.org/x/vuln/cmd/govulncheck ;;
        go-mod-outdated) tool=go:github.com/psampaz/go-mod-outdated ;;
        goreleaser) tool=aqua:goreleaser/goreleaser ;;
        jq) tool=aqua:jqlang/jq ;;
        gh) tool=aqua:cli/cli ;;
        node|npm|npx) tool=core:node ;;
        python|python3) tool=core:python ;;
        pre-commit) tool=aqua:pre-commit/pre-commit ;;
        markdownlint-cli2) tool=npm:markdownlint-cli2 ;;
        *) fail "unknown tool $binary" ;;
    esac
    expected=$(mise which "$binary" --tool "$tool") || fail "$binary has no locked managed provider"
    [ -x "$expected" ] || fail "$binary is not installed at $expected"
    actual=$(command -v "$binary") || fail "$binary is not on PATH"
    # Identical spellings already identify the required provider without extra processes.
    [ "$actual" = "$expected" ] && continue
    # Resolve directory aliases such as /tmp -> /private/tmp on macOS.
    expected="$(cd "$(dirname "$expected")" && pwd -P)/$(basename "$expected")"
    actual="$(cd "$(dirname "$actual")" && pwd -P)/$(basename "$actual")"
    [ "$actual" = "$expected" ] || fail "$binary resolves to $actual, expected $expected"
done
