package evolution

import (
	"encoding/json"

	"github.com/mdcfrancis/flow/storage"
)

// SystemObjective is the ONE goal every evolvable substrate pulls toward — cell genotypes,
// boundaries, prompts, and policy alike. It is injected into every optimizer so nothing drifts
// to a local proxy; correctness is never traded for efficiency.
const SystemObjective = `The whole system evolves toward ONE goal: build the most CORRECT and ` +
	`TOKEN-EFFICIENT system possible. CORRECT = every cell passes its acceptance and faithfully ` +
	`serves the intent (the render looks right, the behavior is right). TOKEN-EFFICIENT = reach ` +
	`correctness with the FEWEST model tokens and mutation steps — follow the most DIRECT path ` +
	`(like a depth-first descent that commits to one subtree at a time), never re-deriving what is ` +
	`already known nor exploring broadly when a focused path exists, and never burning tokens on a ` +
	`cell that is not making progress. Token efficiency is a FIRST-CLASS goal: a correct solution ` +
	`reached with fewer tokens is strictly better. Improve CORRECTNESS first (never trade it away); ` +
	`among correct options, always prefer the most TOKEN-EFFICIENT.`

// Policy is the set of the system's tunable JUDGEMENT CALLS — the scalars that were hard-coded
// in the Go core. It is an evolvable artifact (like prompts): resolved at use-time, tunable by
// operator feedback and by the policy optimizer, and epoch-checkpointed. Every field is BOUNDED
// (clamp) so a bad tune can never drive the loop to a pathological value — bounds are to policy
// what the adversarial validator is to prompts.
type Policy struct {
	// MaxStallRetries: build attempts before a stuck cell escalates (recertify → boundary →
	// fracture). Higher = more persistence but more tokens.
	MaxStallRetries int `json:"maxStallRetries"`
	// GrievanceThreshold: malformed-output grievances before a prompt is auto-refined.
	GrievanceThreshold int `json:"grievanceThreshold"`
	// VisionIntervalSec / CodeCriticIntervalSec: how often the (costly) adversarial critics run.
	VisionIntervalSec     int `json:"visionIntervalSec"`
	CodeCriticIntervalSec int `json:"codeCriticIntervalSec"`
}

// DefaultPolicy is the hand-tuned baseline.
func DefaultPolicy() Policy {
	return Policy{
		MaxStallRetries:       3,
		GrievanceThreshold:    3,
		VisionIntervalSec:     120,
		CodeCriticIntervalSec: 180,
	}
}

// clamp keeps every field within safe bounds, so no tune can wedge the loop.
func (p *Policy) clamp() {
	p.MaxStallRetries = clampInt(p.MaxStallRetries, 1, 10)
	p.GrievanceThreshold = clampInt(p.GrievanceThreshold, 1, 20)
	p.VisionIntervalSec = clampInt(p.VisionIntervalSec, 15, 900)
	p.CodeCriticIntervalSec = clampInt(p.CodeCriticIntervalSec, 30, 1800)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

const policyRef = "urn:hdm:system:policy"

// LoadPolicy returns the current policy: the stored override merged onto the defaults (so a
// partial or absent override keeps the defaults), then clamped.
func LoadPolicy(ledger *storage.LedgerEngine) Policy {
	p := DefaultPolicy()
	if ledger == nil {
		return p
	}
	if h, err := ledger.GetRef(policyRef); err == nil {
		if raw, rerr := ledger.ReadBlock(h); rerr == nil {
			_ = json.Unmarshal(raw, &p) // missing fields keep their defaults
		}
	}
	p.clamp()
	return p
}

// SavePolicy clamps and persists a policy override.
func SavePolicy(ledger *storage.LedgerEngine, p Policy) error {
	p.clamp()
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	h, err := ledger.WriteBlock(raw)
	if err != nil {
		return err
	}
	return ledger.UpdateRef(policyRef, h)
}

// PolicyOverridden reports whether a policy override is stored (vs pure defaults).
func PolicyOverridden(ledger *storage.LedgerEngine) bool {
	_, err := ledger.GetRef(policyRef)
	return err == nil
}
