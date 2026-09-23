# Modern Go audit

Date: 2026-09-23

## Scope and method

The audit covered all 274 Go files in the module. The module targets Go 1.26.3
(with the Go 1.26.6 toolchain). I ran the Modern Go Guidelines `list` command
for Go 1.26, read its complete output, and used `explain` for every guideline
listed below before deciding whether to change or defer a match. Searches covered
production code and tests; generated or downloaded guideline files are not part
of this repository.

## Applicable guidelines and findings

| Guideline ID | Finding | Disposition |
| --- | --- | --- |
| `testing_t_context` | 25 calls in eight test files created background contexts instead of contexts tied to the test lifetime. | Fixed in this change. |
| `sync_waitgroup_go` | Five legacy `Add(1)` / goroutine / `Done()` groups remain in concurrency tests; production fan-out code also uses explicit wait-group accounting with launch control. | Defer the mechanical test conversions; retain explicit production accounting where cancellation and launch tracking make `WaitGroup.Go` a poor fit. |
| `errors_as_type` | Four assertions in `cmd/ferret/surface_test.go` use a temporary target plus `errors.As`. | Defer as one small error-handling change. |
| `errors_is` | Two readers compare directly with `io.EOF`. | Defer together with focused reader regression tests because accepting wrapped EOF broadens behavior. |
| `json_omitzero` | 86 numeric or boolean JSON fields still use `omitempty`; time fields already use `omitzero`. | Defer as a serialization-focused change with golden/compatibility validation. |
| `slices_sort` / `slices_sort_func` | 15 ordered-slice helpers and 43 typed `sort.Slice`/`sort.SliceStable` calls remain. Stable sorts and domain-specific multi-key comparators require individual review. | Defer in package-sized changes; do not perform a repository-wide import churn. |
| `range_over_int` | Three simple index loops are candidates; other counted loops have changing bounds, inclusive bounds, or non-unit steps. | Defer with their owning packages. |
| `slices_clone` | Four `append([]T(nil), source...)` copies are direct candidates. Other allocate-and-fill loops transform elements and do not qualify. | Defer as a small slice-helper change. |
| `maps_copy` | Several map population loops were reviewed, but their sources are slices or their values are transformed; no safe direct replacement was found. | No change. |
| `min_max` | Comparison-heavy scoring and mining code was reviewed; branches generally enforce bounds, update related state, or encode tie-breaking rather than merely select one value. | No change. |

The remaining listed Go 1.26 guidelines had no actionable match: no pointer-only
helper suitable for `new(expression)`, benchmark `b.N` loop, split-result
iteration, obsolete loop-variable capture, manual map clear/clone, untyped
atomic operation, `interface{}`, manual duration calculation, or matching
string/byte cut pattern was found. Existing uses of `slices.Sort`, typed atomics,
`errors.Is`, `omitzero`, and `time.Since` were left intact.

## Changes made

All 25 test call sites now derive work from `t.Context()`. Tests that explicitly
exercise cancellation retain their child `WithCancel`, but now parent it to the
test context. This preserves the cancellation being tested while ensuring leaked
or blocked work is canceled when the test ends. No production behavior changed.

## Findings not changed

The deferred findings above are intentionally not bundled into this change.
Serialization tags can affect wire compatibility, sorting conversions touch many
independent packages, and EOF matching broadens accepted errors; each deserves
focused tests and review. The explicit wait groups in production coordinate
semaphores, cancellation, result slots, and launch bookkeeping, so a blanket
rewrite would be broader than the guideline's exact-lifetime case.

## Proposed PR breakdown

1. **Test context lifetime (this change):** adopt `t.Context()` in tests.
2. **Small standard-library idioms:** `errors.AsType`, eligible test wait groups,
   direct slice clones, and the three simple integer ranges.
3. **Reader error matching:** use `errors.Is` for EOF with wrapped-EOF tests.
4. **JSON zero omission:** migrate scalar tags package by package and verify JSON
   fixtures/compatibility.
5. **Typed sorting:** migrate sorts in package-sized batches, preserving stable
   ordering and tie-break behavior.

## Validation

Validation performed:

- `gofmt` on every changed Go file and `git diff --check`: passed.
- `go test ./...`: passed.
- `make check` (`go vet`, golangci-lint, coverage tests, build, and the
  conform-to-SDLC validator): passed.
- `make audit FUZZTIME=5s`: race tests, fuzzing, and duplicate detection passed;
  nilaway was unavailable and skipped by the repository target; `govulncheck`
  could not fetch `vuln.go.dev` because the environment returned HTTP 403.
