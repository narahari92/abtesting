// Package assign is the core of the service: a pure, deterministic mapping
// from (experiment seed, visitor id) to a variant index. It has no
// dependencies and no state. The browser evaluator in web/ab.js implements
// the same algorithm; internal/assign/testdata/golden.json keeps them in sync.
package assign
