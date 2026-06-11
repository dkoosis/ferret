# Boot
updated: 2026-06-10

→ Finish the surprise/seqs miner WIP in internal/mine — `make check` fails until it's clean.

1. `cd ~/Projects/ferret && make check` — 2 lint hits in rank.go (gocognit on RankPatterns, gofmt).
2. rank.go, rank_test.go, surprise.go, main.go all uncommitted mid-flight.
3. Test data exists now: `make corpus N=60` or `ferret ingest -root testdata/corpus`.

✓ done
- gen-corpus CLI + testdata fixtures (committed 23988d8)

‡ traps
- internal/mine WIP breaks lint — don't commit it blind.
