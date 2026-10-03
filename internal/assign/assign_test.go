package assign

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"
)

type goldenFixture struct {
	Buckets int `json:"buckets"`
	FNV1a32 []struct {
		Input string `json:"input"`
		Hash  uint32 `json:"hash"`
	} `json:"fnv1a32"`
	Cases []struct {
		Seed        string   `json:"seed"`
		VisitorID   string   `json:"visitor_id"`
		HashVersion int      `json:"hash_version"`
		WeightsBP   []int    `json:"weights_bp"`
		CoverageBP  int      `json:"coverage_bp"`
		Ranges      [][2]int `json:"ranges"`
		Bucket      int      `json:"bucket"`
		Variant     int      `json:"variant"`
	} `json:"cases"`
}

func loadGolden(t *testing.T) goldenFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var f goldenFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// population returns n visitor ids; the sample size shrinks under -short.
func population(t *testing.T, n int, shape string) []string {
	if testing.Short() {
		n /= 10
	}
	rng := rand.New(rand.NewSource(1))
	ids := make([]string, n)
	for i := range ids {
		switch shape {
		case "numeric":
			ids[i] = fmt.Sprint(i + 1)
		default:
			b := make([]byte, 16)
			rng.Read(b)
			ids[i] = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
		}
	}
	return ids
}

func TestFNV1a32KnownVectors(t *testing.T) {
	// Published FNV-1a 32-bit test vectors.
	for in, want := range map[string]uint32{"": 0x811c9dc5, "a": 0xe40c292c, "foobar": 0xbf9cf968} {
		if got := FNV1a32(in); got != want {
			t.Errorf("FNV1a32(%q) = %#x, want %#x", in, got, want)
		}
	}
}

func TestGolden(t *testing.T) {
	f := loadGolden(t)
	if f.Buckets != Buckets {
		t.Fatalf("fixture buckets %d, package %d", f.Buckets, Buckets)
	}
	for _, h := range f.FNV1a32 {
		if got := FNV1a32(h.Input); got != h.Hash {
			t.Errorf("FNV1a32(%q) = %d, want %d", h.Input, got, h.Hash)
		}
	}
	if len(f.Cases) < 200 {
		t.Fatalf("fixture has only %d cases", len(f.Cases))
	}
	for i, c := range f.Cases {
		ranges := Ranges(c.WeightsBP, c.CoverageBP)
		if fmt.Sprint(ranges) != fmt.Sprint(c.Ranges) {
			t.Errorf("case %d: ranges %v, want %v", i, ranges, c.Ranges)
		}
		b, ok := Bucket(c.Seed, c.VisitorID, c.HashVersion)
		if c.Bucket == -1 {
			if ok {
				t.Errorf("case %d: expected held back before bucketing, got bucket %d", i, b)
			}
		} else if !ok || b != c.Bucket {
			t.Errorf("case %d: bucket %d ok=%v, want %d", i, b, ok, c.Bucket)
		}
		if got := Evaluate(c.Seed, c.VisitorID, c.HashVersion, ranges); got != c.Variant {
			t.Errorf("case %d (%s/%s): variant %d, want %d", i, c.Seed, c.VisitorID, got, c.Variant)
		}
	}
}

func TestRanges(t *testing.T) {
	cases := []struct {
		weights  []int
		coverage int
		want     [][2]int
	}{
		{[]int{5000, 5000}, 10000, [][2]int{{0, 5000}, {5000, 10000}}},
		{[]int{5000, 5000}, 9000, [][2]int{{0, 4500}, {5000, 9500}}},
		{[]int{9000, 1000}, 10000, [][2]int{{0, 9000}, {9000, 10000}}},
		{[]int{3300, 3300, 3400}, 10000, [][2]int{{0, 3300}, {3300, 6600}, {6600, 10000}}},
		{[]int{5000, 5000}, 0, [][2]int{{0, 0}, {5000, 5000}}},
		{[]int{10000}, 10000, [][2]int{{0, 10000}}},
	}
	for _, c := range cases {
		if got := Ranges(c.weights, c.coverage); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("Ranges(%v, %d) = %v, want %v", c.weights, c.coverage, got, c.want)
		}
	}
}

func TestChoose(t *testing.T) {
	ranges := [][2]int{{0, 4500}, {5000, 9500}}
	for bucket, want := range map[int]int{0: 0, 4499: 0, 4500: -1, 4999: -1, 5000: 1, 9499: 1, 9500: -1, 9999: -1} {
		if got := Choose(bucket, ranges); got != want {
			t.Errorf("Choose(%d) = %d, want %d", bucket, got, want)
		}
	}
	if Choose(0, nil) != -1 {
		t.Error("Choose with no ranges must hold back")
	}
}

func TestUnknownHashVersionHeldBack(t *testing.T) {
	for _, v := range []int{0, 2, 99, -1} {
		if _, ok := Bucket("seed", "visitor", v); ok {
			t.Errorf("hash version %d must not bucket", v)
		}
		if got := Evaluate("seed", "visitor", v, Ranges([]int{10000}, 10000)); got != -1 {
			t.Errorf("hash version %d: Evaluate = %d, want -1", v, got)
		}
	}
}

func TestVisitorIDValidation(t *testing.T) {
	valid := []string{"a", "visitor-8841", "user@example.com", " ", "~", strings.Repeat("x", 128)}
	invalid := []string{"", strings.Repeat("x", 129), "tab\there", "new\nline", "café", "\x7f", "emoji\U0001F600"}
	for _, id := range valid {
		if !ValidVisitorID(id) {
			t.Errorf("%q should be valid", id)
		}
	}
	for _, id := range invalid {
		if ValidVisitorID(id) {
			t.Errorf("%q should be invalid", id)
		}
		if _, ok := Bucket("seed", id, HashVersion1); ok {
			t.Errorf("%q must not bucket", id)
		}
	}
}

// chiSquare of observed counts against a uniform expectation.
func chiSquare(counts []int, total int) float64 {
	expected := float64(total) / float64(len(counts))
	var x float64
	for _, c := range counts {
		d := float64(c) - expected
		x += d * d / expected
	}
	return x
}

// TestUniformDistribution hashes a large population into all 10,000 buckets
// and checks the chi-square statistic against its expected value under
// uniformity (mean k-1, standard deviation sqrt(2(k-1))). Numeric ids are the
// case where a single FNV-1a pass fails, which is why the hash is doubled.
func TestUniformDistribution(t *testing.T) {
	for _, shape := range []string{"uuid", "numeric"} {
		ids := population(t, 1_000_000, shape)
		counts := make([]int, Buckets)
		for _, id := range ids {
			b, ok := Bucket("3f9a1c7e5b2d4a6f8c0e1d3b5a7f9c2e", id, HashVersion1)
			if !ok {
				t.Fatalf("unexpected hold back for %q", id)
			}
			counts[b]++
		}
		x := chiSquare(counts, len(ids))
		k := float64(Buckets - 1)
		sd := math.Sqrt(2 * k)
		// Six standard deviations: effectively never fails under uniformity,
		// while a biased hash lands orders of magnitude outside.
		if math.Abs(x-k) > 6*sd {
			t.Errorf("%s ids: chi-square %.0f, expected %.0f ± %.0f", shape, x, k, 6*sd)
		}
		t.Logf("%s ids: n=%d chi-square=%.0f (expected %.0f ± %.0f)", shape, len(ids), x, k, sd)

		// For the design document: the same statistic with a single pass.
		single := make([]int, Buckets)
		for _, id := range ids {
			single[FNV1a32("3f9a1c7e5b2d4a6f8c0e1d3b5a7f9c2e"+id)%Buckets]++
		}
		t.Logf("%s ids: single-pass FNV-1a chi-square=%.0f", shape, chiSquare(single, len(ids)))
	}
}

func TestHonoursWeights(t *testing.T) {
	ids := population(t, 1_000_000, "uuid")
	for _, weights := range [][]int{{5000, 5000}, {9000, 1000}, {3300, 3300, 3400}, {100, 9900}} {
		ranges := Ranges(weights, 10000)
		counts := make([]int, len(weights))
		for _, id := range ids {
			v := Evaluate("seed-weights", id, HashVersion1, ranges)
			if v < 0 {
				t.Fatalf("held back at full coverage: %q", id)
			}
			counts[v]++
		}
		for i, w := range weights {
			got := float64(counts[i]) / float64(len(ids))
			want := float64(w) / Buckets
			if math.Abs(got-want) > 0.005 {
				t.Errorf("weights %v variant %d: share %.4f, want %.4f ± 0.005", weights, i, got, want)
			}
		}
	}
}

func TestCoverageRespected(t *testing.T) {
	ids := population(t, 1_000_000, "uuid")
	for _, coverage := range []int{1000, 5000, 9000} {
		ranges := Ranges([]int{5000, 5000}, coverage)
		admitted := 0
		for _, id := range ids {
			if Evaluate("seed-coverage", id, HashVersion1, ranges) >= 0 {
				admitted++
			}
		}
		got := float64(admitted) / float64(len(ids))
		want := float64(coverage) / Buckets
		if math.Abs(got-want) > 0.005 {
			t.Errorf("coverage %d: admitted %.4f, want %.4f", coverage, got, want)
		}
	}
}

// TestCoverageRampIsSticky: raising coverage must never move a visitor who
// already had a variant.
func TestCoverageRampIsSticky(t *testing.T) {
	ids := population(t, 200_000, "uuid")
	weights := []int{3300, 3300, 3400}
	prev := make([]int, len(ids))
	for i := range prev {
		prev[i] = -1
	}
	for _, coverage := range []int{1000, 2500, 5000, 8000, 10000} {
		ranges := Ranges(weights, coverage)
		for i, id := range ids {
			v := Evaluate("seed-ramp", id, HashVersion1, ranges)
			if prev[i] >= 0 && v != prev[i] {
				t.Fatalf("visitor %q moved from %d to %d when coverage rose to %d", id, prev[i], v, coverage)
			}
			prev[i] = v
		}
	}
}

// TestExperimentsIndependent: two experiments with different seeds bucket the
// same population independently (joint ≈ product of marginals).
func TestExperimentsIndependent(t *testing.T) {
	ids := population(t, 500_000, "uuid")
	ranges := Ranges([]int{5000, 5000}, 10000)
	var joint [2][2]int
	for _, id := range ids {
		a := Evaluate("seed-A", id, HashVersion1, ranges)
		b := Evaluate("seed-B", id, HashVersion1, ranges)
		joint[a][b]++
	}
	n := float64(len(ids))
	for a := 0; a < 2; a++ {
		for b := 0; b < 2; b++ {
			pa := float64(joint[a][0]+joint[a][1]) / n
			pb := float64(joint[0][b]+joint[1][b]) / n
			got := float64(joint[a][b]) / n
			if math.Abs(got-pa*pb) > 0.005 {
				t.Errorf("P(a=%d,b=%d)=%.4f, product of marginals %.4f", a, b, got, pa*pb)
			}
		}
	}
}

func TestDeterministic(t *testing.T) {
	ranges := Ranges([]int{5000, 5000}, 9000)
	first := Evaluate("seed", "visitor-8841", HashVersion1, ranges)
	for i := 0; i < 1000; i++ {
		if Evaluate("seed", "visitor-8841", HashVersion1, ranges) != first {
			t.Fatal("assignment changed between calls")
		}
	}
}

func BenchmarkEvaluate(b *testing.B) {
	ranges := Ranges([]int{5000, 5000}, 9000)
	for i := 0; i < b.N; i++ {
		Evaluate("3f9a1c7e5b2d4a6f8c0e1d3b5a7f9c2e", "9b2c7d1e-4f3a-4b8c-9d0e-1f2a3b4c5d6e", HashVersion1, ranges)
	}
}
