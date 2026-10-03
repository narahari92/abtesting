//go:build ignore

// gen_golden writes testdata/golden.json, the fixture shared by the Go tests
// and web/ab.test.js. Run once with `go run internal/assign/gen_golden.go`
// and commit the result. The fixture is frozen on purpose: a change in its
// expected values means the algorithm changed, which would reshuffle every
// running experiment.
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"

	"variantsvc/internal/assign"
)

type hashCase struct {
	Input string `json:"input"`
	Hash  uint32 `json:"hash"`
}

type assignCase struct {
	Seed        string   `json:"seed"`
	VisitorID   string   `json:"visitor_id"`
	HashVersion int      `json:"hash_version"`
	WeightsBP   []int    `json:"weights_bp"`
	CoverageBP  int      `json:"coverage_bp"`
	Ranges      [][2]int `json:"ranges"`
	Bucket      int      `json:"bucket"`  // -1 when held back before bucketing
	Variant     int      `json:"variant"` // -1 when held back
}

type fixture struct {
	Description string       `json:"description"`
	Buckets     int          `json:"buckets"`
	FNV1a32     []hashCase   `json:"fnv1a32"`
	Cases       []assignCase `json:"cases"`
}

func main() {
	rng := rand.New(rand.NewSource(20240601))
	uuid := func() string {
		b := make([]byte, 16)
		rng.Read(b)
		b[6] = (b[6] & 0x0f) | 0x40
		b[8] = (b[8] & 0x3f) | 0x80
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	}

	seeds := []string{
		"3f9a1c7e5b2d4a6f8c0e1d3b5a7f9c2e",
		"00000000000000000000000000000000",
		"hero-cta", // short, non-hex seed to prove the hash does not care
	}
	visitors := []string{"1", "2", "3", "42", "1000000", "visitor-8841", "visitor-0052", "visitor-1190",
		"user@example.com", "A", "~", " leading space", "trailing space ", "with/slash?and=query&chars",
		"ThEqUiCkBrOwNfOx", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	for i := 0; i < 24; i++ {
		visitors = append(visitors, uuid())
	}
	splits := []struct {
		weights  []int
		coverage int
	}{
		{[]int{5000, 5000}, 10000},
		{[]int{5000, 5000}, 9000},
		{[]int{9000, 1000}, 10000},
		{[]int{3300, 3300, 3400}, 10000},
		{[]int{3300, 3300, 3400}, 1000},
		{[]int{100, 9900}, 10000},
		{[]int{2500, 2500, 2500, 2500}, 5000},
	}

	f := fixture{
		Description: "Shared Go/JS fixture for the assignment algorithm (hash_version 1: double FNV-1a 32, mod 10000). Do not regenerate unless the algorithm changes intentionally.",
		Buckets:     assign.Buckets,
	}
	for _, in := range []string{"", "a", "foobar", "hero-cta", "3f9a1c7e5b2d4a6f8c0e1d3b5a7f9c2evisitor-8841"} {
		f.FNV1a32 = append(f.FNV1a32, hashCase{Input: in, Hash: assign.FNV1a32(in)})
	}

	add := func(seed, v string, hv int, weights []int, coverage int) {
		ranges := assign.Ranges(weights, coverage)
		c := assignCase{Seed: seed, VisitorID: v, HashVersion: hv, WeightsBP: weights, CoverageBP: coverage, Ranges: ranges, Bucket: -1, Variant: -1}
		if b, ok := assign.Bucket(seed, v, hv); ok {
			c.Bucket = b
			c.Variant = assign.Choose(b, ranges)
		}
		f.Cases = append(f.Cases, c)
	}
	// Spread the visitor list over seeds and splits so the fixture stays
	// around 200 cases while covering every combination at least once.
	n := 0
	for _, s := range splits {
		for _, seed := range seeds {
			for i, v := range visitors {
				if (i+n)%3 == 0 {
					add(seed, v, assign.HashVersion1, s.weights, s.coverage)
				}
			}
			n++
		}
	}
	// Unknown hash version: held back, bucket not computed.
	add(seeds[0], "visitor-8841", 99, []int{5000, 5000}, 10000)
	add(seeds[0], "visitor-8841", 0, []int{5000, 5000}, 10000)

	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("internal/assign/testdata/golden.json", append(out, '\n'), 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %d cases\n", len(f.Cases))
}
