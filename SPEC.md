# ferret spec

Numbered requirements, each with the check that shows it is met. The North Star is
[docs/NORTH_STAR.md](docs/NORTH_STAR.md).

## Motif questions in one call (epic ferret-1yj)

An agent asks ferret one question about a motif (which routines hold a token, what a
command tokenizes to, what comes before and after a token, every row) and gets the
answer from one call, with no grep, no saved file and no wrong-flag retry. Measure: the
ferret calls an agent needs to answer one question decrease.

- **R1.** `--limit all` on any command with CommonFlags MUST print every row, the same
  rows as `--limit=-1`; a value that is neither `all` nor an integer MUST exit non-zero
  naming both forms. (ferret-38r)
  Check: `TestNgramsLimit_AllMatchesMinusOne_When_FlagParsed`,
  `TestLimitFlag_ExitsUsage_When_ValueIsNeitherAllNorInteger` (cmd/ferret/limit_test.go).
- **R2.** `--contains=<substring>` on ngrams, seqs and graph MUST keep only rows where
  some token holds the substring, MUST NOT change counts or order, and MUST count JSON
  totals after the filter. (ferret-f6i)
  Check: `TestNgramsContains_MatchesGrep_When_FilterSet` and the other tests in
  cmd/ferret/contains_test.go.
- **R3.** `ferret vocab [substring]` MUST list each token under a lens with its
  occurrence and stream counts, as the miners see them after the lens flags apply.
  (ferret-mta)
  Check: `TestVocab_Substring_PrintsRowWithCounts` (cmd/ferret/vocab_test.go).
- **R4.** `ferret around <token>` MUST print the tokens before and after an exact token,
  with occurrence and stream counts from the raw grams (not the suppressed set), and the
  top 10 gapped sequences that hold it; a substring matching several tokens MUST exit
  non-zero listing them. (ferret-hp5)
  Check: `TestAround_AfterCountEqualsCountGrams_When_FilterSuppresses`,
  `TestAround_EndsWithSeqsThroughToken`, `TestAround_AmbiguousSubstring_ErrorsNamingMatches`
  (cmd/ferret/around_test.go).

With the new flags unset, ngrams, seqs and graph output is byte-identical to before
(NFR-14); each new flag and command appears in `--help` and README's command list
(NFR-15, `readme_test.go`).
