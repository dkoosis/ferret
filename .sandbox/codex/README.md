# Codex Sandbox Notes

Operational notes for the OpenAI Codex cloud sandbox running against ferret.

## Definition of "done" in sandbox

Sandbox = fast iteration, not full gate. Ship claims bounded by what the sandbox can prove:

- **OK here**: targeted tests, small/medium refactors, static reasoning, unit-test authoring, changed-package validation.
- **Defer to CI / local**: full repo race runs, broad `no regressions anywhere` claims, perf/soak/flake analysis, cross-platform guarantees.

Rule of thumb: `build + tests covering the changed packages pass` is sufficient evidence to ship from sandbox. Full `make audit` is CI's job.

## Canonical commands

| Want | Run |
|------|-----|
| Fast validation (vet + lint + test + build) | `make check` |
| Exhaustive validation | `make audit` *(expensive — prefer CI)* |
| Validate toolchain presence | `make doctor` |

## Known sandbox constraints

- **Prebuilt `golangci-lint`** (`.sandbox/bin/linux-{amd64,arm64}/golangci-lint`) may be built with an older Go than `go.mod` declares. `setup.sh` warns on mismatch.
- **Ephemeral build cache**: `setup.sh` runs `warm_test_cache`, but expect cold-compile cost on fresh containers.
- **Constrained compute**: race / integration suites are expensive in-sandbox. Reason locally, confirm in CI.

## Asking Codex for work

Phase requests:

1. Code change + compile.
2. Targeted tests for the change.
3. (Optional) broader validation.

Prefer package-scoped tasks over "harden the whole repo." For concurrency/perf/race claims, ask for a minimal reproducer in-sandbox and full confirmation in CI.
