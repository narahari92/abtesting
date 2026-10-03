package assign

import "strconv"

// Buckets is the number of equal-probability buckets a visitor can land in.
// Weights and coverage are expressed in basis points over this same range,
// so all arithmetic stays in integers and Go and JavaScript cannot disagree
// by rounding.
const Buckets = 10000

// HashVersion1 is double FNV-1a 32-bit: the decimal string of
// fnv1a32(seed + visitor) is hashed again and reduced mod Buckets.
// Hashing twice fixes the weak low-bit mixing of a single pass on short,
// similar inputs such as numeric ids. Evaluators that see a version they
// do not implement must treat the visitor as held back.
const HashVersion1 = 1

// MaxVisitorIDLen bounds visitor ids. Together with the printable-ASCII rule
// it guarantees the browser (UTF-16 code units) and Go (bytes) hash the
// same input.
const MaxVisitorIDLen = 128

// FNV1a32 is the standard 32-bit FNV-1a hash over the bytes of s.
func FNV1a32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// ValidVisitorID reports whether id is 1..MaxVisitorIDLen printable ASCII
// characters (0x20..0x7E).
func ValidVisitorID(id string) bool {
	if len(id) == 0 || len(id) > MaxVisitorIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] > 0x7E {
			return false
		}
	}
	return true
}

// Bucket maps a visitor to an integer in [0, Buckets) for one experiment.
// The second return is false when the hash version is unknown or the visitor
// id is invalid; callers must then treat the visitor as held back.
func Bucket(seed, visitorID string, hashVersion int) (int, bool) {
	if hashVersion != HashVersion1 || !ValidVisitorID(visitorID) {
		return 0, false
	}
	first := FNV1a32(seed + visitorID)
	second := FNV1a32(strconv.FormatUint(uint64(first), 10))
	return int(second % Buckets), true
}

// Ranges turns per-variant weights (basis points, summing to Buckets) and a
// coverage (basis points) into half-open bucket ranges [start, end). Each
// variant's slice is shrunk from its left edge by coverage, so held-back
// visitors fall in the gaps at the right of each slice. Raising coverage only
// extends ranges to the right: visitors already assigned never move.
func Ranges(weightsBP []int, coverageBP int) [][2]int {
	ranges := make([][2]int, len(weightsBP))
	start := 0
	for i, w := range weightsBP {
		ranges[i] = [2]int{start, start + w*coverageBP/Buckets}
		start += w
	}
	return ranges
}

// Choose returns the index of the first range containing bucket, or -1 when
// the bucket falls in a coverage gap (held back).
func Choose(bucket int, ranges [][2]int) int {
	for i, r := range ranges {
		if bucket >= r[0] && bucket < r[1] {
			return i
		}
	}
	return -1
}

// Evaluate is Bucket followed by Choose. It returns -1 for held back,
// unknown hash version, or invalid visitor id.
func Evaluate(seed, visitorID string, hashVersion int, ranges [][2]int) int {
	b, ok := Bucket(seed, visitorID, hashVersion)
	if !ok {
		return -1
	}
	return Choose(b, ranges)
}
