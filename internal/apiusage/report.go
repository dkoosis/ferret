package apiusage

import "sort"

// SessionRow is one session's ledger, ranked by weighted cost — the "which
// sessions actually cost money" list. Agent rows fold into their session: a
// subagent's spend is spend the session incurred.
type SessionRow struct {
	Session  string  `json:"session"`
	Totals   Totals  `json:"totals"`
	Weighted float64 `json:"weighted"`
	Agents   int     `json:"agents"` // distinct agent ids seen, main thread included

	agents map[string]struct{}
}

// Report is the corpus-wide token ledger: measured spend, the one ferret number
// an outside source can contradict.
type Report struct {
	Totals   Totals       `json:"totals"`
	Sessions int          `json:"sessions"`
	Models   []ModelRow   `json:"models"`
	Rows     []SessionRow `json:"rows"`
}

// ModelRow is per-model spend. Models differ in price per token, so a weighted
// figure that pools them is only as good as the assumption that the mix held —
// the split is here so that assumption is visible rather than buried.
type ModelRow struct {
	Model    string  `json:"model"`
	Totals   Totals  `json:"totals"`
	Weighted float64 `json:"weighted"`
}

// Aggregate reads the ledger artifact and folds it into the corpus report.
func Aggregate(path string) (*Report, error) {
	rep := &Report{}
	sessions := map[string]*SessionRow{}
	models := map[string]*ModelRow{}

	err := Read(path, func(r *Row) error {
		rep.Totals.Add(r)

		s, ok := sessions[r.Session]
		if !ok {
			s = &SessionRow{Session: r.Session, agents: map[string]struct{}{}}
			sessions[r.Session] = s
		}
		s.Totals.Add(r)
		s.Weighted += r.Weighted()
		s.agents[r.Agent] = struct{}{}

		m, ok := models[r.Model]
		if !ok {
			m = &ModelRow{Model: r.Model}
			models[r.Model] = m
		}
		m.Totals.Add(r)
		m.Weighted += r.Weighted()
		return nil
	})
	if err != nil {
		return nil, err
	}

	rep.Sessions = len(sessions)
	rep.Rows = make([]SessionRow, 0, len(sessions))
	for _, s := range sessions {
		s.Agents = len(s.agents)
		rep.Rows = append(rep.Rows, *s)
	}
	sort.Slice(rep.Rows, func(i, j int) bool {
		if rep.Rows[i].Weighted != rep.Rows[j].Weighted {
			return rep.Rows[i].Weighted > rep.Rows[j].Weighted
		}
		return rep.Rows[i].Session < rep.Rows[j].Session
	})

	rep.Models = make([]ModelRow, 0, len(models))
	for _, m := range models {
		rep.Models = append(rep.Models, *m)
	}
	sort.Slice(rep.Models, func(i, j int) bool {
		if rep.Models[i].Weighted != rep.Models[j].Weighted {
			return rep.Models[i].Weighted > rep.Models[j].Weighted
		}
		return rep.Models[i].Model < rep.Models[j].Model
	})
	return rep, nil
}

// Share is one bucket's percentage of a weighted total — the column that
// reorders the picture, because the buckets differ in price by ~50x end to end.
func Share(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return 100 * part / whole
}
