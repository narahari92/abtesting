package payload

import (
	"encoding/json"
	"sort"
	"strconv"

	"variantsvc/internal/assign"
	"variantsvc/internal/experiment"
)

// Payload is the compiled, public, cacheable configuration for one site.
// It is what the browser evaluator and GET /v1/assign both consume.
type Payload struct {
	Site        string       `json:"site"`
	Version     int64        `json:"version"`
	Experiments []Experiment `json:"experiments"`
}

// Experiment is a running experiment with ranges precomputed, so an
// evaluator does one hash and one range scan.
type Experiment struct {
	Key         string    `json:"key"`
	URLPath     string    `json:"url_path"`
	Seed        string    `json:"seed"`
	HashVersion int       `json:"hash_version"`
	Ranges      [][2]int  `json:"ranges"`
	Variants    []Variant `json:"variants"`
}

// Variant carries the key (the decision) and optional content.
type Variant struct {
	Key     string         `json:"key"`
	Content map[string]any `json:"content"`
}

// Empty is the payload for an unknown or suspended site. It has the same
// shape as a real one so the snippet behaves identically.
func Empty(site string) *Payload {
	return &Payload{Site: site, Version: 0, Experiments: []Experiment{}}
}

// Compile builds the payload for a site from its experiments. Only servable
// experiments (running, valid, all variants approved) are included; the
// keys of experiments that were running but not servable are returned so
// the caller can log them. Output is ordered by experiment key and variant
// position, so identical input always yields identical bytes.
func Compile(site string, version int64, exps []experiment.Experiment) (*Payload, []string) {
	p := &Payload{Site: site, Version: version, Experiments: []Experiment{}}
	var skipped []string
	for i := range exps {
		e := &exps[i]
		if e.Status != experiment.StatusRunning {
			continue
		}
		if !e.Servable() {
			skipped = append(skipped, e.Key)
			continue
		}
		variants := make([]experiment.Variant, len(e.Variants))
		copy(variants, e.Variants)
		sort.SliceStable(variants, func(a, b int) bool { return variants[a].Position < variants[b].Position })
		weights := make([]int, len(variants))
		out := make([]Variant, len(variants))
		for j, v := range variants {
			weights[j] = v.WeightBP
			content := v.Content
			if content == nil {
				content = map[string]any{}
			}
			out[j] = Variant{Key: v.Key, Content: content}
		}
		urlPath, _ := experiment.NormalizeURLPath(e.URLPath)
		p.Experiments = append(p.Experiments, Experiment{
			Key:         e.Key,
			URLPath:     urlPath,
			Seed:        e.Seed,
			HashVersion: e.HashVersion,
			Ranges:      assign.Ranges(weights, e.CoverageBP),
			Variants:    out,
		})
	}
	sort.Slice(p.Experiments, func(a, b int) bool { return p.Experiments[a].Key < p.Experiments[b].Key })
	return p, skipped
}

// Marshal returns the canonical JSON bytes. encoding/json sorts map keys,
// and Compile orders slices, so the bytes are a pure function of the input.
func (p *Payload) Marshal() ([]byte, error) { return json.Marshal(p) }

// ETag is the HTTP entity tag for the payload: its version, quoted.
func (p *Payload) ETag() string { return strconv.Quote(strconv.FormatInt(p.Version, 10)) }

// Find returns the experiment with the given key, or nil.
func (p *Payload) Find(key string) *Experiment {
	for i := range p.Experiments {
		if p.Experiments[i].Key == key {
			return &p.Experiments[i]
		}
	}
	return nil
}

// Assignment is one evaluated experiment for a visitor.
type Assignment struct {
	Experiment string         `json:"experiment"`
	Variant    string         `json:"variant"`
	Content    map[string]any `json:"content"`
}

// Evaluate runs the reference algorithm for one visitor over the payload.
// Held-back and unknown-version experiments are omitted. If keys is
// non-empty only those experiments are evaluated; unknown keys are ignored.
// If urlPath is non-empty only experiments on that page are evaluated,
// mirroring what the browser does with location.pathname.
func (p *Payload) Evaluate(visitorID string, keys []string, urlPath string) []Assignment {
	out := make([]Assignment, 0, len(p.Experiments))
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	page, pageOK := experiment.NormalizeURLPath(urlPath)
	for i := range p.Experiments {
		e := &p.Experiments[i]
		if len(want) > 0 && !want[e.Key] {
			continue
		}
		if urlPath != "" && (!pageOK || e.URLPath != page) {
			continue
		}
		idx := assign.Evaluate(e.Seed, visitorID, e.HashVersion, e.Ranges)
		if idx < 0 || idx >= len(e.Variants) {
			continue
		}
		out = append(out, Assignment{Experiment: e.Key, Variant: e.Variants[idx].Key, Content: e.Variants[idx].Content})
	}
	return out
}
