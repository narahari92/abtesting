package experiment

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"variantsvc/internal/assign"
)

// Status is the lifecycle state of an experiment. Only running experiments
// are compiled into a site's payload.
type Status string

const (
	StatusDraft    Status = "draft"
	StatusRunning  Status = "running"
	StatusPaused   Status = "paused"
	StatusArchived Status = "archived"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusRunning, StatusPaused, StatusArchived:
		return true
	}
	return false
}

// Variant sources.
const (
	SourceManual = "manual"
	SourceLLM    = "llm"
)

// Site statuses.
const (
	SiteActive    = "active"
	SiteSuspended = "suspended"
)

// Limits shared by validation and the admin API.
const (
	MaxKeyLen         = 64
	MaxNameLen        = 200
	MaxDescriptionLen = 2000
	MaxVariants       = 20
	// MaxContentBytes bounds a variant's serialized content.
	MaxContentBytes = 8 * 1024
)

var keyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidKey reports whether k is a usable site, experiment or variant key:
// lowercase letters, digits and hyphens, 1..64 characters, not starting
// with a hyphen. Keys appear in URLs, data-ab attributes and query strings.
func ValidKey(k string) bool { return keyRE.MatchString(k) }

// Site is a tenant. APIKeyHash is the SHA-256 of the private site API key;
// the key itself is never stored.
type Site struct {
	ID             string    `json:"-"`
	Key            string    `json:"key"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	APIKeyHash     string    `json:"-"`
	AllowedOrigins []string  `json:"allowed_origins"`
	AssignRPSLimit int       `json:"assign_rps_limit"`
	EventsRPSLimit int       `json:"events_rps_limit"`
	PayloadVersion int64     `json:"payload_version"`
	CreatedAt      time.Time `json:"created_at"`
}

// Variant is one arm of an experiment. Content is an arbitrary JSON object
// delivered with the decision; the service never interprets it.
type Variant struct {
	Key       string         `json:"key"`
	WeightBP  int            `json:"weight_bp"`
	IsControl bool           `json:"is_control"`
	Content   map[string]any `json:"content"`
	Source    string         `json:"source"`
	Approved  bool           `json:"approved"`
	Position  int            `json:"position"`
}

// Experiment is the configuration unit a visitor is bucketed into.
type Experiment struct {
	ID          string    `json:"-"`
	SiteID      string    `json:"-"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      Status    `json:"status"`
	Seed        string    `json:"seed"`
	HashVersion int       `json:"hash_version"`
	CoverageBP  int       `json:"coverage_bp"`
	Variants    []Variant `json:"variants"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Validate checks the invariants every stored experiment must satisfy.
// It returns all problems found, joined, so an API caller can fix them in
// one round.
func (e *Experiment) Validate() error {
	var errs []error
	if !ValidKey(e.Key) {
		errs = append(errs, fmt.Errorf("key %q must match %s", e.Key, keyRE))
	}
	if len(e.Name) > MaxNameLen {
		errs = append(errs, fmt.Errorf("name longer than %d characters", MaxNameLen))
	}
	if len(e.Description) > MaxDescriptionLen {
		errs = append(errs, fmt.Errorf("description longer than %d characters", MaxDescriptionLen))
	}
	if !e.Status.Valid() {
		errs = append(errs, fmt.Errorf("status %q is not one of draft, running, paused, archived", e.Status))
	}
	if e.Seed == "" {
		errs = append(errs, errors.New("seed is empty"))
	}
	if e.HashVersion != assign.HashVersion1 {
		errs = append(errs, fmt.Errorf("hash_version %d is not supported", e.HashVersion))
	}
	if e.CoverageBP < 0 || e.CoverageBP > assign.Buckets {
		errs = append(errs, fmt.Errorf("coverage_bp %d must be within 0..%d", e.CoverageBP, assign.Buckets))
	}
	if len(e.Variants) == 0 {
		errs = append(errs, errors.New("at least one variant is required"))
	}
	if len(e.Variants) > MaxVariants {
		errs = append(errs, fmt.Errorf("more than %d variants", MaxVariants))
	}
	sum, controls := 0, 0
	seen := make(map[string]bool, len(e.Variants))
	for _, v := range e.Variants {
		if !ValidKey(v.Key) {
			errs = append(errs, fmt.Errorf("variant key %q must match %s", v.Key, keyRE))
		}
		if seen[v.Key] {
			errs = append(errs, fmt.Errorf("variant key %q is duplicated", v.Key))
		}
		seen[v.Key] = true
		if v.WeightBP < 0 || v.WeightBP > assign.Buckets {
			errs = append(errs, fmt.Errorf("variant %q weight_bp %d must be within 0..%d", v.Key, v.WeightBP, assign.Buckets))
		}
		sum += v.WeightBP
		if v.IsControl {
			controls++
		}
		if v.Source != SourceManual && v.Source != SourceLLM {
			errs = append(errs, fmt.Errorf("variant %q source %q must be manual or llm", v.Key, v.Source))
		}
	}
	if len(e.Variants) > 0 && sum != assign.Buckets {
		errs = append(errs, fmt.Errorf("variant weights sum to %d basis points, must be exactly %d", sum, assign.Buckets))
	}
	if len(e.Variants) > 0 && controls != 1 {
		errs = append(errs, fmt.Errorf("exactly one control variant is required, found %d", controls))
	}
	return errors.Join(errs...)
}

// Servable reports whether the experiment should be compiled into the
// payload: running, valid, and every variant approved. An unapproved
// variant would leave a hole in the allocation, so the whole experiment
// stays out until the customer approves or removes it.
func (e *Experiment) Servable() bool {
	if e.Status != StatusRunning || e.Validate() != nil {
		return false
	}
	for _, v := range e.Variants {
		if !v.Approved {
			return false
		}
	}
	return true
}

// WeightsBP returns variant weights in position order.
func (e *Experiment) WeightsBP() []int {
	w := make([]int, len(e.Variants))
	for i, v := range e.Variants {
		w[i] = v.WeightBP
	}
	return w
}

// NewSeed returns 16 random bytes, hex-encoded: the per-experiment value
// that decorrelates bucketing across experiments and tenants.
func NewSeed() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// CanTransition encodes the lifecycle: draft → running → paused ⇄ running,
// anything but archived → archived. Archived is terminal, and nothing goes
// back to draft because visitors may already have been exposed.
func CanTransition(from, to Status) bool {
	switch from {
	case StatusDraft:
		return to == StatusRunning || to == StatusArchived
	case StatusRunning:
		return to == StatusPaused || to == StatusArchived
	case StatusPaused:
		return to == StatusRunning || to == StatusArchived
	}
	return false
}

// StartBlockers lists why an experiment cannot move to running, or nothing.
func (e *Experiment) StartBlockers() []string {
	var out []string
	if err := e.Validate(); err != nil {
		out = append(out, err.Error())
	}
	for _, v := range e.Variants {
		if !v.Approved {
			out = append(out, fmt.Sprintf("variant %q is an unapproved draft; approve or remove it", v.Key))
		}
	}
	return out
}
