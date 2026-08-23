package apiusage

import "sort"

// SessionRow is one session's ledger, ranked by dollars — the "which sessions
// actually cost money" list. Agent rows fold into their session: a subagent's
// spend is spend the session incurred.
//
// Cost lives in Totals.USD, accumulated per row at that row's model price, so a
// session mixing Opus and Haiku ranks on what it actually cost rather than on a
// pooled token count that treats the two as interchangeable.
type SessionRow struct {
	Session string `json:"session"`
	Totals  Totals `json:"totals"`
	Agents  int    `json:"agents"` // distinct agent ids seen, main thread included

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

// ModelRow is per-model spend. Models differ in base price by 10x, so the split
// is what makes a pooled dollar figure auditable — and it names any model whose
// price is missing rather than letting its calls vanish into a total.
type ModelRow struct {
	Model  string `json:"model"`
	Totals Totals `json:"totals"`
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
		s.agents[r.Agent] = struct{}{}

		m, ok := models[r.Model]
		if !ok {
			m = &ModelRow{Model: r.Model}
			models[r.Model] = m
		}
		m.Totals.Add(r)
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
		if rep.Rows[i].Totals.USD != rep.Rows[j].Totals.USD {
			return rep.Rows[i].Totals.USD > rep.Rows[j].Totals.USD
		}
		return rep.Rows[i].Session < rep.Rows[j].Session
	})

	rep.Models = make([]ModelRow, 0, len(models))
	for _, m := range models {
		rep.Models = append(rep.Models, *m)
	}
	sort.Slice(rep.Models, func(i, j int) bool {
		if rep.Models[i].Totals.USD != rep.Models[j].Totals.USD {
			return rep.Models[i].Totals.USD > rep.Models[j].Totals.USD
		}
		return rep.Models[i].Model < rep.Models[j].Model
	})
	return rep, nil
}

// Share is one bucket's percentage of a total — the column that reorders the
// picture, because the buckets differ in price by ~50x within a model.
func Share(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return 100 * part / whole
}
