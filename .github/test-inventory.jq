# Inputs are Go JSON events: listing is not execution. Selectors apply to top-level tests.
def key: [.Package, .Test];
. as $events |
[$listed[] | select(.Action == "output" and (.Output | test("^Test[^[:space:]]+\n$"))) |
  {Package, Test: (.Output | rtrimstr("\n"))}] | unique as $tests |
[$events[] | select(.Test and .Action == "run") | {Package, Test}] | unique as $started |
[$events[] | select(.Test and (.Action == "pass" or .Action == "fail" or .Action == "skip")) |
  {Package, Test, outcome: .Action}] as $finished |
{
  selector: $selector,
  process_exit: $exit,
  listing_available: ($listed | length > 0),
  results: [$finished[] | . as $result |
    . + {level: (if (.Test | contains("/")) then "subtest" else "top-level" end)} |
    if .outcome == "skip" then . + {reason: ([$events[] |
      select(.Package == $result.Package and .Test == $result.Test and .Action == "output") | .Output] | join(""))} else . end],
  excluded: [$tests[] | select(.Test | test($selector) | not)],
  unrun: [$tests[] | select(.Test | test($selector)) | . as $test |
    select($started | any(key == ($test | key)) | not)],
  unfinished: [$started[] | . as $test | select($finished | any(key == ($test | key)) | not)],
  note: "Counts include only observed results; dynamic subtests cannot be listed beforehand. Empty/missing execution is not passing evidence."
}
