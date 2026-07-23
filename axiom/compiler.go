package axiom

// Package axiom implements the Natural-Language Target Axiom Compiler (roadmap
// Step 11): it turns an unstructured human requirement into a structured,
// type-clamped target stored in the user-targets bucket, and propagates the
// target's importance into cell saliency so the Hamiltonian rewards cells that
// serve user goals.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdcfrancis/flow/manifest"
	"github.com/mdcfrancis/flow/storage"
)

// TargetsURN is the mutable reference holding the user target set.
const TargetsURN = "urn:hdm:user:targets"

// Target is a compiled requirement.
type Target struct {
	ID         string   `json:"id"`
	Intent     string   `json:"intent"`
	DomainTags []string `json:"domain_tags"`
	Saliency   float64  `json:"saliency"`
	Assertions []string `json:"assertions"`
}

// TargetSet is the persisted collection of targets.
type TargetSet struct {
	Targets []Target `json:"targets"`
}

// SaliencyFor returns the summed saliency of targets whose domain tags
// intersect the given cell tags — the reward weight for cells serving user goals.
func (s *TargetSet) SaliencyFor(cellTags []string) float64 {
	tagset := map[string]bool{}
	for _, t := range cellTags {
		tagset[t] = true
	}
	var total float64
	for _, tgt := range s.Targets {
		for _, tag := range tgt.DomainTags {
			if tagset[tag] {
				total += tgt.Saliency
				break
			}
		}
	}
	return total
}

// Reasoner is the cognitive-engine boundary (inference.LocalModelClient satisfies it).
type Reasoner interface {
	InvokeReasoning(ctx context.Context, systemPrompt, userContext string) (string, error)
}

const systemPrompt = `You compile a natural-language system requirement into a strict JSON target axiom.
Output ONLY a single JSON object, no prose and no code fences, with fields:
  "intent": a short string summarizing the requirement,
  "domain_tags": an array of lowercase tag strings the requirement concerns,
  "saliency": a number between 0 and 1 indicating importance,
  "assertions": an array of short, testable assertion strings.`

// Compiler compiles NL requirements into targets on the ledger.
type Compiler struct {
	ledger *storage.LedgerEngine
	model  Reasoner
}

// NewCompiler binds a target compiler to a ledger and cognitive engine.
func NewCompiler(ledger *storage.LedgerEngine, model Reasoner) *Compiler {
	return &Compiler{ledger: ledger, model: model}
}

// Compile turns a natural-language requirement into a structured target,
// appends it to the user-targets set, and returns it.
func (c *Compiler) Compile(ctx context.Context, nl string) (*Target, error) {
	resp, err := c.model.InvokeReasoning(ctx, systemPrompt, nl)
	if err != nil {
		return nil, fmt.Errorf("axiom reasoning failed: %w", err)
	}
	js := extractJSON(resp)
	if js == "" {
		return nil, fmt.Errorf("no JSON object in model output")
	}
	var t Target
	if err := json.Unmarshal([]byte(js), &t); err != nil {
		return nil, fmt.Errorf("decode target: %w", err)
	}
	if t.Saliency < 0 {
		t.Saliency = 0
	}
	if t.Saliency > 1 {
		t.Saliency = 1
	}
	sum := sha256.Sum256([]byte(t.Intent))
	t.ID = "axiom:" + hex.EncodeToString(sum[:])[:12]

	set, err := c.Load()
	if err != nil {
		return nil, err
	}
	// Replace any existing target with the same ID (idempotent re-compile).
	replaced := false
	for i := range set.Targets {
		if set.Targets[i].ID == t.ID {
			set.Targets[i] = t
			replaced = true
			break
		}
	}
	if !replaced {
		set.Targets = append(set.Targets, t)
	}
	if err := c.save(set); err != nil {
		return nil, err
	}
	return &t, nil
}

// Load returns the current target set (empty if none).
func (c *Compiler) Load() (*TargetSet, error) {
	h, err := c.ledger.GetRef(TargetsURN)
	if err != nil {
		return &TargetSet{}, nil
	}
	raw, err := c.ledger.ReadBlock(h)
	if err != nil {
		return nil, fmt.Errorf("read targets: %w", err)
	}
	var set TargetSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("decode targets: %w", err)
	}
	return &set, nil
}

func (c *Compiler) save(set *TargetSet) error {
	raw, err := json.Marshal(set)
	if err != nil {
		return fmt.Errorf("encode targets: %w", err)
	}
	h, err := c.ledger.WriteBlock(raw)
	if err != nil {
		return fmt.Errorf("persist targets: %w", err)
	}
	return c.ledger.UpdateRef(TargetsURN, h)
}

// ApplyTo updates each cell's descriptor saliency to the summed saliency of the
// targets that concern its domain tags, so friction discovery and the
// Hamiltonian reflect user priorities. Cell genotype/phenotype are unchanged.
func (c *Compiler) ApplyTo(repo *manifest.Repository, cellURNs []string) error {
	set, err := c.Load()
	if err != nil {
		return err
	}
	for _, urn := range cellURNs {
		desc, err := repo.Load(urn)
		if err != nil {
			continue
		}
		sal := set.SaliencyFor(desc.Semantics.DomainTags)
		if sal == desc.Saliency {
			continue
		}
		geno, err := repo.Genotype(desc)
		if err != nil {
			continue
		}
		pheno, err := repo.Phenotype(desc)
		if err != nil {
			continue
		}
		h, _, err := repo.PutCell(urn, geno, pheno, desc.Semantics, sal)
		if err != nil {
			return err
		}
		if err := repo.SeedRef(urn, h); err != nil {
			return err
		}
	}
	return nil
}

// extractJSON isolates the first balanced {...} object in a string.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
