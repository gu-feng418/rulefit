# Real-world audit: `alibaba/open-code-review`

What `rulefit` reported when it was first pointed at a configuration it had not
grown up with, and — more usefully — what that exercise got wrong.

The short version: **the audit found no defect in that repository, and three bugs
in `rulefit`.** Both halves are recorded here, because a tool whose selling point
is provable claims should be honest about the ones it got wrong.

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

## The result

After the fixes described below, the audit reports no errors and no warnings:

```
115 file extensions, 54 path patterns, 79 extensions with a language rule

NOTE (1)
  [unrouted-extension] 36 of 115 allowlisted extensions route to no language
      rule and fall back to default.md
```

Full output: [`opencodereview-report.txt`](opencodereview-report.txt).

That is the correct answer for this configuration. Every rule in the map is
reachable, no pattern is dead, and the declaration order is a deliberate
specific-before-general arrangement with no rule being shadowed.

The single note is a product decision rather than a defect: `.cs`, `.rb`, `.sh`,
`.vue`, `.sql`, `.lua` and 30 others are reviewable but routed to `default.md`.
`--strict` exists for teams that consider that a defect instead.

## The false positive, and why it mattered

The first run reported this as an **error**:

```
[dead-pattern] pattern "**/*.R" matches no allowlisted file type, so rule "r.md" can never be selected
[unreachable-rule] rule document "r.md" is declared but no allowlisted file type can reach it
```

The reasoning looked sound: the rule map declares `"**/*.R"` on line 37, the
allowlist contains `".r"` and not `".R"`, and `rulefit` compares byte-exactly. On a
textual reading the two lines disagree.

The reading was wrong. `alibaba/open-code-review` folds case on **both** sides
before comparing:

* `internal/config/rules/system_rules.go` resolves every rule with
  `doublestar.Match(strings.ToLower(p), lowerPath)`.
* `internal/config/allowlist/allowed_ext.go` consults its map with
  `allowedRuleExts[strings.ToLower(filepath.Ext(s))]`, and `initMap` lower-cases
  every entry as it loads it.

So `"**/*.R"` is compared as `"**/*.r"`, `plot.R` is compared as `plot.r`, and the
rule fires. `r.md` is reachable. There is nothing to fix upstream.

This is worth stating plainly because it is the failure mode a configuration
auditor is most likely to have: **`rulefit` reads text, while the tool it audits
may normalize that text before acting on it.** Upstream had already handled the
case question deliberately — see `#75` ("match canonical Cargo.toml patterns
case-insensitively") and `#844` (the same bug fixed in `FileFilter`) — so the
mismatch was in my mental model, not in their code.

### What changed in `rulefit`

A new audit-configuration key makes the audited tool's comparison rule explicit,
because guessing it wrong produces findings that are wrong in both directions:

```json
{ "case_insensitive": true }
```

When it is set, patterns, probe paths, directory prefixes and extension suffixes
are all folded through one `Config.Normalize` call before being compared, mirroring
what the audited tool does. `examples/opencodereview.json` sets it, because that is
what `alibaba/open-code-review` does.

The default is `false`, which audits the configuration as written. That default is
deliberate: silently folding case would hide a genuine mismatch in a tool that
really does compare byte-exactly, and `TestRunCaseSensitivityIsHonoured` pins that
behaviour.

## The bugs in `rulefit` that this audit exposed

| # | Symptom | Cause | Pinned by |
| --- | --- | --- | --- |
| 1 | Every whole-file rule (`**/pom.xml`, `**/package.json`) reported as dead | Probes were derived from the allowlist alone, never from the literals the patterns name | `TestRunKeepsWholeFileNameRulesAlive`, `TestRunReportsExtensionNotInAllowlist` |
| 2 | Names built from a starred pattern were invisible (`**/Cargo.toml` over an allowlist holding `.toml`) | `**` split the segment into runs, and only the two whole-segment shapes were combined with allowlisted extensions | `TestRunKeepsWholeFileNameRulesAlive` |
| 3 | Reported lines were short by the depth of the selected JSON subtree (said 34 for a pattern on 37) | Line came from the pattern's index inside `path_rule_map`, ignoring the three lines above it | `TestRunReportsSourceLines`, `TestLoadKeepsDeclarationOrder` |
| 4 | Five `shadowed-rule` warnings, with the direction stated backwards | Only overlap was measured, so the healthy specific-before-general arrangement was reported as a defect | `TestRunFlagsTheGeneralBeforeSpecificOrdering`, `TestRunKeepsWholeFileNameRulesAlive` |
| 5 | A UTF-8 byte-order mark made a configuration file unparseable | Written by a tool that prepends one; invisible in a diff, and a test that only asserted the exit code did not catch it | `TestNoUTF8BOM`, `TestCheckFailsOnTheRealWorldExample` |

Bug 4 was a semantics problem, not a coding slip. For a first-match-wins map the
only ordering worth warning about is "the earlier pattern already claims every path
the later one claims", because that is when the later rule can never be selected.
The reverse — earlier narrower, later a fallback — is the arrangement the map is
supposed to have. The kinds are now named for the fact (`earlier-unreachable`,
`later-unreachable`, `partial`) rather than for letters, because the old
`a-shadows-b` / `b-shadows-a` names were the source of the mistake.

Two related correctness fixes came out of the same debugging:

* **Containment is measured before the allowlist filter, not after.** Narrowing both
  sides to the same admissible subset first made a wider pattern look exactly as
  wide as a narrower one, and inverted the direction.
* **Both `\n` and `\r\n` count as a line ending**, so a Windows checkout and a Git
  object report the same line for the same pattern.

And one testing lesson: `runCapture` only captured stdout while the CLI reports
errors on stderr, so a test asserting "this exits 2" passed for a *parse failure*
it was not written for — which is exactly how bug 5 survived. It now captures both
and asserts the message rather than just the code.

## What this leaves for upstream

One readability observation, not a defect: `"**/*.R": "r.md"` is a mixed-case
pattern over a lower-case allowlist entry, and it only works because the matcher
folds case.

```
$ rulefit explain --config examples/opencodereview.json --path analysis/plot.R
path:      analysis/plot.R
reviewed:  true
rule:      r.md
matched:   "**/*.R" (line 37)
```

Full output: [`opencodereview-explain-uppercase-R.txt`](opencodereview-explain-uppercase-R.txt).
The normal case, for contrast, is
[`opencodereview-explain-package-json.txt`](opencodereview-explain-package-json.txt),
where `**/package.json` wins over its `**/*.{json,json5}` fallback exactly as
intended.
