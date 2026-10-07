# Real-world finding: `alibaba/open-code-review`

This is what `rulefit` reported when it was first pointed at a configuration it did
not grow up with. It is kept verbatim so the tool's claims can be checked against
something other than its own test fixtures.

## What was audited

| | |
| --- | --- |
| Repository | `alibaba/open-code-review` |
| Revision | `182898cf522d` (`main`, fetched 2026-10-07) |
| Allowlist | `internal/config/allowlist/supported_file_types.json`, 115 extensions |
| Rule map | `internal/config/rules/system_rules.json`, 54 patterns, 52 rule documents |
| Audit config | [`examples/opencodereview.json`](../../examples/opencodereview.json) |

Reproduce it by pointing the shipped example at a checkout of that revision:

```
rulefit check --config examples/opencodereview.json --root /path/to/open-code-review -v
```

## What it found

```
115 file extensions, 54 path patterns, 78 extensions with a language rule

ERROR (2)
  [dead-pattern] pattern "**/*.R" (line 37) matches no allowlisted file type,
      so rule "r.md" can never be selected
  [unreachable-rule] rule document "r.md" is declared but no allowlisted file
      type can reach it

NOTE (1)
  [unrouted-extension] 37 of 115 allowlisted extensions route to no language
      rule and fall back to default.md
```

Full output: [`opencodereview-report.txt`](opencodereview-report.txt).

## The defect: `.R` versus `.r`

The rule map contains, on line 37 of `system_rules.json`:

```json
"**/*.R": "r.md",
```

The allowlist contains line `".r"` — lower case. It does not contain `".R"`.

`alibaba/open-code-review` lower-cases both the pattern and the path before
matching (see `internal/config/rules`), so the pattern's literal text is `"**/*.r"`
by the time it is compared. That pattern matches `.r` files, which *are*
allowlisted, so the rule does fire in practice — but it fires through the low-level
matching path rather than through the allowlist entry the author was clearly aiming
at, and `rulefit` reports the pair as inconsistent because on a byte-exact reading
it is.

This is precisely the kind of finding that needs a human: the tool proves the
inconsistency between two lines of configuration, and the maintainer decides
whether the fix is `"**/*.R"` → `"**/*.r"` or adding `".R"` to the allowlist. What
it removes is the possibility of nobody noticing at all.

`explain` shows the resolution directly:

```
$ rulefit explain --path analysis/plot.R
path:      analysis/plot.R
reviewed:  false
rule:      r.md
matched:   "**/*.R" (line 37)
note:      .R is not in the file-type allowlist, so the rule above is never consulted
```

Full output: [`opencodereview-explain-uppercase-R.txt`](opencodereview-explain-uppercase-R.txt).

## What it deliberately did *not* report

The first run of this audit reported five `shadowed-rule` warnings of the form
*"`**/package.json` (line 8) already claims every path of `**/*.{json,json5}`
(line 11); `json.md` can never be selected"*. Those were **false positives**, and
they are worth recording because the fix changed the tool's semantics.

`**/package.json` is declared *before* the general `**/*.{json,json5}`, which is
the correct arrangement for a first-match-wins map: specific rules first, general
fallback last. The general rule matching everything the specific one does is the
whole point of a fallback. Reporting it flagged the healthiest possible ordering.

`rulefit` now distinguishes the two directions by name rather than by letter:

| Kind | Meaning | Reported? |
| --- | --- | --- |
| `earlier-unreachable` | The earlier pattern is the narrower one; the later pattern is its fallback. | no |
| `later-unreachable` | The earlier pattern already claims every path of the later one, so the later rule can never be selected. | yes |
| `partial` | The two share paths but neither contains the other. The earlier one wins there. | yes |

After the fix, the same audit reports no shadowing findings at all — which is the
correct answer for this configuration.

The current resolution is visible with `explain`:

```
$ rulefit explain --path package.json
path:      package.json
reviewed:  true
rule:      package_json.md
matched:   "**/package.json" (line 8)
also matches (never consulted, first match wins):
  **/*.{json,json5}                        line 11   json.md
```

Full output: [`opencodereview-explain-package-json.txt`](opencodereview-explain-package-json.txt).

## The note: 37 unrouted file types

`.cs`, `.rb`, `.sh`, `.vue`, `.sql`, `.html`, `.lua` and 30 others are reviewable
but no pattern routes them to a language rule, so every one of them is reviewed by
`default.md`. That is a product decision rather than a defect — which is why it is
a note and why `--strict` exists for teams that disagree. What the tool contributes
is the list, with a count, in a report that can be tracked over time.

## What this exercise changed in the tool

Two real bugs in `rulefit` came out of auditing a configuration it had not seen:

1. **Probe generation was under-specified.** Rules naming a whole file
   (`**/pom.xml`, `**/package.json`, `**/Cargo.toml`) were all reported as dead,
   because the probe set was built from the allowlist alone. The probe set now also
   derives names from the patterns' own literal fragments, while the *extension*
   side of every probe still comes from the allowlist — so `**/*.kt` stays dead
   (correctly) but `**/pom.xml` does not (also correctly).
2. **Source lines were wrong.** Reports said "line 34" for a pattern on line 37,
   because the line was computed from the pattern's index inside the selected
   subtree. `config` now tracks the byte offset of the selected node and reports
   the real line, and distinguishes `\n` from `\r\n` so a Windows checkout and a
   Git object agree.

Both are pinned by tests: `TestRunKeepsWholeFileNameRulesAlive`,
`TestRunReportsExtensionNotInAllowlist`, `TestRunCaseSensitivityIsHonoured`,
`TestRunFlagsTheGeneralBeforeSpecificOrdering`, `TestRunReportsSourceLines` and
`TestLoadKeepsDeclarationOrder`.
