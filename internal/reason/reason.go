// Package reason normalises and caps the reason lists that the engines expose
// (SPEC v2 §30, §31; plan M6c ADR D19, closes M6b-H1).
//
// A reason list is evidence, not prose: it travels to the API, to the UI and
// into the persisted rows, and the tests assert its literal strings. Two
// properties are therefore non-negotiable:
//
//  1. BOUNDED. An engine that degrades for many independent reasons (a missing
//     price, a stale beta, an untrustworthy growth, an absent valuation, six
//     missing metrics...) would otherwise produce a paragraph nobody reads, and
//     the important reason would be diluted. MODEL_MAX_REASONS (default 12)
//     caps it.
//  2. STABLE. The same degradation must produce the same list, in the same
//     order, in every run and every consumer: trim whitespace, drop empties,
//     remove duplicates and keep the order of FIRST appearance (the order in
//     which the engine discovered them, which is the order of the pipeline
//     stages). Sorting alphabetically would be stable too, but it would destroy
//     the causal order that makes the list readable.
//
// An unknown reason string is NOT rejected: reasons are persisted data that
// outlives the binary that wrote them, and a newer engine's vocabulary must
// still survive a round trip through an older reader.
package reason

import (
	"strings"

	"github.com/miky/abys-invest/internal/modelcfg"
)

// EnvMaxReasons is the env var that caps the reason list per result.
const EnvMaxReasons = "MODEL_MAX_REASONS"

// DefaultMaxReasons is the cap when MODEL_MAX_REASONS is unset or invalid.
const DefaultMaxReasons = 12

// maxReasonsFloor / maxReasonsCeiling keep an accidental 0 (which would hide
// every reason) or 100000 (which would remove the cap entirely) from silently
// disabling the bound.
const (
	maxReasonsFloor   = 1
	maxReasonsCeiling = 100
)

// MaxReasons returns the configured cap (MODEL_MAX_REASONS, default 12),
// clamped to [1, 100].
func MaxReasons() int {
	return clampMax(modelcfg.EnvIntRange(EnvMaxReasons, DefaultMaxReasons, maxReasonsFloor, maxReasonsCeiling))
}

func clampMax(n int) int {
	if n < maxReasonsFloor {
		return maxReasonsFloor
	}
	if n > maxReasonsCeiling {
		return maxReasonsCeiling
	}
	return n
}

// Normalize returns the canonical reason list for a result: trimmed, without
// empty entries, without duplicates, in first-appearance order, capped at
// MODEL_MAX_REASONS. It returns nil when nothing survives, so that an empty list
// disappears from the JSON instead of showing up as `"reasons": []`.
func Normalize(reasons []string) []string {
	return NormalizeLimit(reasons, MaxReasons())
}

// NormalizeLimit is Normalize with an EXPLICIT cap. The backtest report uses it
// so that its output does not depend on the environment of the machine that ran
// the replay (§27 determinism).
func NormalizeLimit(reasons []string, max int) []string {
	if len(reasons) == 0 {
		return nil
	}
	if max < 1 {
		max = 1
	}
	seen := make(map[string]struct{}, len(reasons))
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
		if len(out) == max {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeMap is the same contract for the per-metric reasons of quality and
// relative (a metric carries exactly one reason, so the map is cleaned key by
// key and empty reasons are deleted).
func NormalizeMap(reasons map[string]string) map[string]string {
	if len(reasons) == 0 {
		return nil
	}
	out := make(map[string]string, len(reasons))
	for k, v := range reasons {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
