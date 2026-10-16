#!/bin/bash
# Exercise the actual project recipe against disposable global/system settings.
# Downloads only the named base set; never uses an operator's global tool store.
set -euo pipefail
cd "$(dirname "$0")/.."
project=$PWD
sandbox=$(mktemp -d "${TMPDIR:-/tmp}/rdhpf-tools.XXXXXX")
trap 'rm -rf "$sandbox"' EXIT
mkdir -p "$sandbox"/{home,global,system,data,state,config}
export HOME="$sandbox/home"
export MISE_GLOBAL_CONFIG_FILE="$sandbox/global/config.toml"
export MISE_SYSTEM_CONFIG_FILE="$sandbox/system/config.toml"
export MISE_CONFIG_DIR="$sandbox/config"
export MISE_DATA_DIR="$sandbox/data"
export MISE_STATE_DIR="$sandbox/state"
# Share verified download cache, not installed tools. Test the project's policy,
# rather than allowing the calling CI environment to hide a missing setting.
unset MISE_AUTO_INSTALL MISE_LOCKED MISE_LOCKED_SCOPES
printf '[tools]\n"aqua:BurntSushi/ripgrep" = "14.1.0"\n' > "$MISE_GLOBAL_CONFIG_FILE"
printf '[tools]\n"aqua:sharkdp/fd" = "10.2.0"\n' > "$MISE_SYSTEM_CONFIG_FILE"
cp "$MISE_GLOBAL_CONFIG_FILE" "$sandbox/global.before"
cp "$MISE_SYSTEM_CONFIG_FILE" "$sandbox/system.before"
cp mise.lock "$sandbox/project.before"
mise trust

MISE_LOCKED_SCOPES=project mise install --locked core:go aqua:golangci/golangci-lint aqua:jqlang/jq
[ ! -e "$sandbox/global/mise.lock" ]
[ ! -e "$sandbox/system/mise.lock" ]
# An existing unlocked global lock must also remain byte-identical.
printf '# unrelated user lock\nlockfile_version = 3\n' > "$sandbox/global/mise.lock"
cp "$sandbox/global/mise.lock" "$sandbox/global-lock.before"
MISE_LOCKED_SCOPES=project mise install --locked core:go aqua:golangci/golangci-lint aqua:jqlang/jq
mise exec -- scripts/check-tools.sh go gofmt golangci-lint jq
mise exec -- make check

# Exercise shell activation and its actual hook, without profile modification.
eval "$(mise activate bash)"
eval "$(mise hook-env --shell bash)"
scripts/check-tools.sh go gofmt golangci-lint jq
# Missing optional commands must not be automatically installed by activation.
if command -v markdownlint-cli2 >/dev/null 2>&1; then
    echo 'optional Markdown tool unexpectedly available' >&2
    exit 1
fi
if markdownlint-cli2 --version; then
    echo 'activation installed the missing optional command' >&2
    exit 1
fi
mise ls --installed --json > "$sandbox/installed.json"
jq -e 'keys | sort == ["aqua:golangci/golangci-lint", "aqua:jqlang/jq", "go"]' "$sandbox/installed.json"
cat "$sandbox/installed.json"
cmp mise.lock "$sandbox/project.before"
cmp "$MISE_GLOBAL_CONFIG_FILE" "$sandbox/global.before"
cmp "$MISE_SYSTEM_CONFIG_FILE" "$sandbox/system.before"
cmp "$sandbox/global/mise.lock" "$sandbox/global-lock.before"
[ ! -e "$sandbox/system/mise.lock" ]

# Run copies of the real recipe/config in a disposable project for destructive
# negative controls; the checked-out project lock is never removed or rewritten.
fixture="$sandbox/project"
mkdir -p "$fixture/scripts" "$sandbox/ambient"
cp mise.toml Makefile "$fixture/"
cp scripts/check-tools.sh "$fixture/scripts/"
(
    cd "$fixture"
    mise trust
    if MISE_LOCKED_SCOPES=project mise install --locked core:go; then
        echo 'missing project lock accepted' >&2
        exit 1
    fi
    [ ! -e mise.lock ]
)
cp mise.lock "$fixture/"
printf '#!/bin/sh\nprintf ambient > "%s"\nexit 0\n' "$sandbox/ambient-called" > "$sandbox/ambient/go"
chmod +x "$sandbox/ambient/go"
(
    cd "$fixture"
    export PATH="$sandbox/ambient:$PATH"
    if make build GOOS=linux GOARCH=amd64 BUILD_DIR="$sandbox/out"; then
        echo 'ambient compiler accepted' >&2
        exit 1
    fi
    [ ! -e "$sandbox/ambient-called" ]
    export MISE_DATA_DIR="$sandbox/missing-data"
    if mise exec -- make build GOOS=linux GOARCH=amd64 BUILD_DIR="$sandbox/out"; then
        echo 'missing managed compiler accepted' >&2
        exit 1
    fi
    [ ! -e "$sandbox/ambient-called" ]
)
printf 'Project lock, optionality, activation, global coexistence and ambient rejection passed.\n'
