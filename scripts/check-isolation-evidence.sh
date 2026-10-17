#!/bin/bash
# Actual owned-phase guard: exact package/name, executed test, completed package.
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/check-tools.sh jq
jq -e -s --rawfile required .github/required-isolation-tests.txt '
  . as $events |
  ($required | split("\n") | map(select(length > 0) | split("|"))) as $names |
  ($names | length) == 3 and
  all($names[]; .[0] as $package | .[1] as $name |
    ([$events[] | select(.Package == $package and .Test == $name and .Action != "output") | .Action] == ["run", "pass"]) and
    ([$events[] | select(.Package == $package and (.Test // "") == "" and .Action != "output") | .Action] == ["start", "pass"]) and
    ([$events | to_entries[] | select(.value.Package == $package and .value.Test == $name and .value.Action == "pass") | .key][0] <
      [$events | to_entries[] | select(.value.Package == $package and (.value.Test // "") == "" and .value.Action == "pass") | .key][0])
  )
' "$@"
