# testdata — synthetic transcript corpus

Two ways to feed ferret without touching `~/.claude/projects`.

## Static fixtures — `testdata/corpus/`

Hand-crafted, in the `<project-slug>/<session>.jsonl` layout `transcript.Walk`
expects. Small enough to read; each session plants one shape:

| Session | Plants |
|---|---|
| `sess-routine`   | the canonical `Read → Edit → go_test → git_add → git_commit` routine, twice |
| `sess-friction`  | a retry chain (`go_test!` × 2 → pass) + a compound `cfail` (`go vet && go test`) |
| `sess-explore`   | a `Task` spawn, an `isMeta` line (dropped), and a subagent transcript |
| `sess-explore/subagents/agent-explore-01` | sidechain stream, keyed `(session, agent)` — never interleaves into the parent |

Point ferret at it:

```
ferret ingest -root testdata/corpus -data /tmp/fx
ferret graph  -data /tmp/fx -loops      # shows the friction bounce + subagent Grep⇄Read
```

These double as golden inputs — assert on miner output if you wire up e2e tests.

## Generator — `cmd/gen-corpus`

A standalone CLI that emits a **deterministic** corpus at any scale. Same
`-seed` + flags → byte-identical files (timestamps come from a fixed epoch plus
line sequence, never the wall clock).

```
go run ./cmd/gen-corpus -out /tmp/corpus -sessions 60 -seed 7
ferret ingest -root /tmp/corpus
# or just:
make corpus N=60
```

Archetype mix (weighted): `feature` routines dominate so n-grams/seqs rank them;
`friction` gives retry chains; `explore` gives noisy context; `withSubagent`
gives a sidechain file. At ≥60 sessions the default miner thresholds
(min-count=5, min-support=20) clear and every miner lights up.

## SWE-agent corpus — `testdata/swe-agent/`

A second corpus type with **ground-truth outcomes**, for validating the
ranking signal. Each JSONL row is one trajectory = one stream; the row carries
`target` (issue resolved, bool) + `exit_status`. The adapter maps the
role-tagged trajectory onto the canonical `Event` (Project=`swe-agent`,
Session=`instance_id`), routes bash actions through `shellnorm` so tokens line
up with the CC vocabulary (`sh:python`, `sh:git_diff`), and writes a separate
`outcomes.jsonl` sidecar (outcomes are per-stream, not per-event).

```
ferret ingest -source swe-agent -root testdata/swe-agent/sample.jsonl -data /tmp/swe
ferret rank     -data /tmp/swe -min-support 1     # same buckets as CC
ferret validate -data /tmp/swe -min-support 1 -min-streams 1   # buckets × outcome
```

`sample.jsonl` plants three runs: a clean resolved run (`search_dir → open →
edit → python → submit`, target=true), a failing retry-loop run (three failing
`python manage.py test`, target=false), and an aborted run with a bad shell
command (target=false). The adapter is tolerant of field-name variation —
`instance_id`/`instance`/`id`, `target`/`resolved`, `trajectory`/`messages`/
`history`, and a trajectory that's either an array or a JSON-encoded string.

### Acquiring real data (outside the binary)

The Go binary never talks to HuggingFace — no parquet dep. Materialize a local
JSONL sample with duckdb, then point `-root` at it. Start with ~1k rows; pull
the full 80k only if the signal validates:

```sql
COPY (SELECT instance_id, model_name, target, exit_status, trajectory
      FROM 'hf://datasets/nebius/SWE-agent-trajectories/**/*.parquet' LIMIT 1000)
TO 'swe-sample.jsonl';
```

The dataset is CC-BY-4.0. If the real `trajectory` column shape differs from the
fixtures, the decoder's tolerance (above) should absorb most of it; widen
`internal/sweagent/row.go` if a new spelling appears.
