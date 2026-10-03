// Package results aggregates exposures and conversions into per-variant
// counts for the dashboard: exposures, converted visitors, conversion rate
// and a per-goal breakdown. It deliberately stops at counts; statistical
// testing is a documented next step.
package results

import (
	"fmt"
	"time"

	"variantsvc/internal/experiment"
	"variantsvc/internal/store"
)

// Variant is one row of the results table.
type Variant struct {
	Key         string             `json:"key"`
	IsControl   bool               `json:"is_control"`
	WeightBP    int                `json:"weight_bp"`
	Exposures   int                `json:"exposures"`
	Conversions int                `json:"conversions"` // distinct exposed visitors who converted
	Rate        float64            `json:"rate"`        // conversions / exposures
	ByGoal      map[string]int     `json:"by_goal"`
	ValueByGoal map[string]float64 `json:"value_by_goal"`
}

// Report is the response of GET /v1/admin/experiments/{key}/results.
type Report struct {
	Experiment              string    `json:"experiment"`
	Status                  string    `json:"status"`
	Control                 string    `json:"control"`
	Goal                    string    `json:"goal,omitempty"`
	Goals                   []string  `json:"goals"`
	Variants                []Variant `json:"variants"`
	TotalExposures          int       `json:"total_exposures"`
	TotalConversions        int       `json:"total_conversions"`
	UnattributedConversions int       `json:"unattributed_conversions"`
	Notes                   []string  `json:"notes"`
	GeneratedAt             time.Time `json:"generated_at"`
}

// Build assembles a report from the experiment definition and the raw
// counts. Variants come from the definition so arms with no exposures
// still appear; counts for unknown variant keys (removed variants) are
// ignored.
func Build(exp *experiment.Experiment, counts map[string]*store.VariantCounts, unattributed int, goals []string, goal string, now time.Time) Report {
	r := Report{Experiment: exp.Key, Status: string(exp.Status), Goal: goal, Goals: goals, UnattributedConversions: unattributed, Notes: []string{}, GeneratedAt: now}
	if r.Goals == nil {
		r.Goals = []string{}
	}
	for _, v := range exp.Variants {
		if v.IsControl {
			r.Control = v.Key
		}
		c := counts[v.Key]
		if c == nil {
			c = &store.VariantCounts{}
		}
		row := Variant{Key: v.Key, IsControl: v.IsControl, WeightBP: v.WeightBP, Exposures: c.Exposures, Conversions: c.Converted,
			ByGoal: c.ByGoal, ValueByGoal: c.ValueByGoal}
		if row.ByGoal == nil {
			row.ByGoal = map[string]int{}
		}
		if row.ValueByGoal == nil {
			row.ValueByGoal = map[string]float64{}
		}
		if c.Exposures > 0 {
			row.Rate = float64(c.Converted) / float64(c.Exposures)
		}
		r.TotalExposures += c.Exposures
		r.TotalConversions += c.Converted
		r.Variants = append(r.Variants, row)
	}
	if r.TotalExposures == 0 {
		r.Notes = append(r.Notes, "No exposures recorded yet.")
	}
	if unattributed > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d conversion(s) had no matching exposure and are not counted.", unattributed))
	}
	return r
}
