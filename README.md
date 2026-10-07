# rulefit

`rulefit` audits the *"which files do we review, and which rule applies to them"*
configuration of a code-review or static-analysis tool.

Most such tools are configured with two things: an allowlist of file types they
are willing to look at, and a map from path globs to per-domain rules. Both grow
by hundreds of lines. Nothing checks that they still fit together, so the map
accumulates rules that can never fire, patterns that match nothing, and pairs
whose winner depends on which line happens to come first.

`rulefit` is that check.

```
$ rulefit check --config examples/opencodereview.json --root path/to/repo

rulefit: path/to/repo
  115 file extensions, 55 path patterns, 79 extensions with a language rule

ERROR (8)
  [dead-pattern] 7 patterns match no allowlisted file type and can never be
      selected: **/*.R (r.md), **/pom.xml (pom_xml.md), ...
  [unreachable-rule] rule document "package_json.md" is declared but no
      allowlisted file type can reach it

WARNING (5)
  [shadowed-rule] "**/package.json" (line 52) covers everything
      "**/*.{json,json5}" (line 35) claims and more; json.md wins on its own paths
      witness: package.json
```

## What it checks

| Code | Meaning | Severity |
| --- | --- | --- |
| `shadowed-rule` | Two patterns compete for the same paths. The resolver stops at the first match, so the later rule never fires for those files. | warning |
| `dead-pattern` | The pattern matches no allowlisted file type, so its rule can never be selected. | error |
| `unreachable-rule` | A rule document is declared but no allowlisted file type can reach it. | error |
| `unrouted-extension` | A file type is reviewed but routes to no language rule and falls back to the default. | note, or error with `--strict` |
| `missing-rule-document` | A pattern points at a rule document that does not exist. | error |
| `unbalanced-pattern` | The pattern uses braces the tool does not treat as alternation, so it silently means something else. | error |

Every finding either carries a **witness** — a concrete path that both patterns
match, checked with the matcher before it is reported — or is marked
`[unproven]`. There is no third state in which the tool claims certainty it does
not have.

## Usage

```
rulefit check   --config rulefit.json [--root DIR] [--strict] [-v]
                [--format text|json|sarif] [--fail-on error|warning|note]

rulefit explain --config rulefit.json --path package.json
```

`explain` answers the reverse question, which is the one you actually ask while
debugging:

```
$ rulefit explain --config examples/opencodereview.json --path package.json
path:      package.json
reviewed:  true
rule:      package_json.md
matched:   "**/package.json" (line 8)
also matches (never consulted, first match wins):
  **/*.{json,json5}                        line 11   json.md
```

Exit status is `0` when nothing fails, `1` when findings fail the run, and `2`
for usage errors, so it drops straight into CI:

```yaml
- run: go run ./cmd/rulefit check --config .rulefit.json --strict
```

## What it found on its first real configuration

Pointing it at `alibaba/open-code-review` turned up a genuine inconsistency: the
rule map declares `"**/*.R": "r.md"` on line 37, the allowlist contains `".r"` but
not `".R"`, and the tool lower-cases patterns before matching — so the rule and its
allowlist entry disagree, and `r.md` reads as unreachable.

That audit also found two real bugs in `rulefit` itself, both now pinned by tests:
every whole-file rule (`**/pom.xml`, `**/package.json`) was reported as dead
because probes were derived from the allowlist alone, and every reported line
number was off by the depth of the selected JSON subtree.

The full report, the reasoning, and the false positives that were removed rather
than shipped are written up in
[docs/findings/opencodereview-2026-10.md](docs/findings/opencodereview-2026-10.md).

## Configuration

`rulefit` is not tied to one project. A small audit configuration says which
files hold the two halves and where inside them:

```json
{
  "allowlist": { "file": "internal/config/allowlist/supported_file_types.json", "selector": "[]" },
  "rules": {
    "file": "internal/config/rules/system_rules.json",
    "default_selector": "default_rule",
    "map_selector": "path_rule_map"
  },
  "docs_dir": "internal/config/rules/rule_docs",
  "ignore_extensions": [],
  "placeholder_extensions": [],
  "ignore_patterns": []
}
```

* `selector` is a dotted path into the JSON document (`config.file_types`).
  `[]` means the document itself is the array.
* `docs_dir` enables the reachability and missing-document checks. Leave it out
  if rules are not files.
* `ignore_extensions` lists file types that are *expected* to fall through to the
  default rule, so they stop being reported.
* `placeholder_extensions` lists entries that exist only to satisfy a formatter
  or a compound extension, and are excluded from the unrouted check.
* `ignore_patterns` silences intentional overlaps.

Declaration order is preserved when the path-to-rule map is read. It is not an
implementation detail: for a first-match-wins resolver, the order *is* the
behaviour, and `rulefit` reports findings against line numbers in that order.

## The glob dialect

| Syntax | Meaning |
| --- | --- |
| `*` | any run of characters within one path segment |
| `?` | exactly one character |
| `[a-z]`, `[!0-9]` | one character from a set, or outside it |
| `{a,b}` | alternation, may nest, expands before matching |
| `**` | zero or more whole path segments |
| `\x` | the literal character `x` |

Matching is byte-oriented and **case-sensitive**. This matters more than it
sounds: the first real configuration audited contained `"**/*.R": "r.md"` against
an allowlist entry of `".r"`, which is the inconsistency
[docs/findings/opencodereview-2026-10.md](docs/findings/opencodereview-2026-10.md)
is about. Real tools often lowercase patterns and paths before matching; if yours
does, note that `rulefit` audits the *text* of the configuration, so it reports the
mismatch and leaves the decision to you — either fix the pattern or change the
matching.

## Scope and non-goals

Honest limits, each of which the tool would rather report as `[unproven]` than
guess at:

* **It reads configuration, not code.** No path is checked against the working
  tree. A pattern that matches no *existing* file is not a finding; a pattern
  that matches no *allowlisted file type* is.
* **Witness search is bounded.** At most 512 concrete paths are derived per
  pattern pair, and `*` is only instantiated as empty or `x`. Pairs whose overlap
  needs a longer shared string are reported as `[unproven]`, never as exact.
* **`.d.ts`-style compound extensions** are treated as one suffix, so a pattern
  like `**/*.ts` matching `a.d.ts` is not considered when deciding whether an
  allowlisted extension is routed.
* **Negated character classes** are compared by enumerating the printable ASCII
  range, not by exact set algebra.
* **Not a linter for the rule documents themselves**, and not a
  "are my review rules any good" tool.

## Status

Early but usable: the engine, the CLI, tests and one real-configuration audit are
in place. The collision engine decides overlap with a product construction over
the two globs, and containment is measured by constructing paths and checking them
with the matcher rather than asserted, so a finding is only ever reported after
concrete verification. Every non-trivial behavioural claim in the tests is a
property (`TestSegmentsOverlapAgreesWithMatching`, `TestFindOverlap`,
`TestRunKeepsWholeFileNameRulesAlive`) rather than a golden string, so correctness
does not depend on one particular config.

The first real audit also found three bugs in this tool, all now pinned by tests.
That history is in [docs/design.md](docs/design.md) and
[docs/findings/opencodereview-2026-10.md](docs/findings/opencodereview-2026-10.md)
rather than tidied away, because a tool whose selling point is provable claims
should be honest about how it earned them.

Planned next, in order:

1. More adapters, each with a fixture in `examples/`: a `.yaml`-configured
   linter, and a tool whose allowlist is an inline array.
2. `--baseline` to accept today's findings and fail only on new ones, which is
   what makes adoption possible on a large existing configuration.
3. A GitHub Action that posts the report as a PR comment.

## AI assistance

Parts of this repository were written with AI assistance, and the maintainer
reviews and can explain every line. The design rationale — why overlap detection
is a product construction instead of a probe heuristic, and which bugs the
property tests and the first real audit caught — is written up in
[docs/design.md](docs/design.md). Bug reports that point at places where the
reasoning is wrong are especially welcome.

## Development

```
go test ./...          # unit tests and the property checks
go vet ./...
go run ./cmd/rulefit check --config examples/opencodereview.json --root ../path/to/repo
```

No third-party dependencies: the glob engine is part of this repository, and so
is everything else.

## License

MIT. See [LICENSE](LICENSE).
