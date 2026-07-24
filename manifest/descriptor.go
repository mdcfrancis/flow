package manifest

// Package manifest realizes the node-descriptor topology:
// the immutable, content-addressed record binding a cell's
// genotype (WAT source), phenotype (compiled WASM), dependencies, semantic
// intent, and saliency. Descriptors are stored as CAS blocks; a mutable URN
// reference points at the active descriptor, so a cell's genotype and phenotype
// are always linked and independently addressable.

import (
	"encoding/json"
	"fmt"

	"github.com/mdcfrancis/flow/storage"
)

// DependencyConstraint records a directed edge to another cell.
type DependencyConstraint struct {
	TargetURN      string `json:"target_urn"`
	DependencyHash string `json:"dependency_hash"`
	IsHotPath      bool   `json:"is_hot_path"`
}

// ImportRef identifies a host import by module and name.
type ImportRef struct {
	Module string `json:"module"`
	Name   string `json:"name"`
}

// SemanticManifest captures the behavioral contract a mutation must preserve.
type SemanticManifest struct {
	FunctionalIntent string   `json:"functional_intent"`
	InputInvariants  []string `json:"input_invariants"`
	OutputInvariants []string `json:"output_invariants"`
	DomainTags       []string `json:"domain_tags"`
	// EffectfulImports are host calls with external side effects (e.g. LLM
	// reasoning) that a mutation must not drop. Their effects cannot be
	// captured by deterministic replay, so they are protected structurally: an
	// optimized candidate must call each at least as often as the baseline.
	EffectfulImports []ImportRef `json:"effectful_imports,omitempty"`
}

// NodeDescriptor is the immutable manifest node for a single cell.
type NodeDescriptor struct {
	IdentityURN   string                 `json:"identity_urn"`
	GenotypeHash  string                 `json:"genotype_hash"`  // CAS hash of the genotype source (Flux program or WAT)
	PhenotypeHash string                 `json:"phenotype_hash"` // CAS hash of the compiled WASM
	Dependencies  []DependencyConstraint `json:"dependencies"`
	Semantics     SemanticManifest       `json:"semantics"`
	Saliency      float64                `json:"current_saliency"`
}

// Repository stores and loads node descriptors on top of the CAS ledger.
type Repository struct {
	ledger *storage.LedgerEngine
}

// NewRepository binds a repository to a ledger.
func NewRepository(ledger *storage.LedgerEngine) *Repository {
	return &Repository{ledger: ledger}
}

// PutCell persists a genotype (WAT source) and phenotype (compiled bytecode) as
// immutable CAS blocks, assembles a node descriptor linking them, writes the
// descriptor block, and returns the descriptor's content hash. It does NOT move
// any reference pointer — the caller decides when to make it live (directly for
// seeding, or via the MVCC coordinator for an evolutionary hot-swap).
func (r *Repository) PutCell(urn, watSource string, bytecode []byte, sem SemanticManifest, saliency float64) (string, *NodeDescriptor, error) {
	genotypeHash, err := r.ledger.WriteBlock([]byte(watSource))
	if err != nil {
		return "", nil, fmt.Errorf("persist genotype: %w", err)
	}
	phenotypeHash, err := r.ledger.WriteBlock(bytecode)
	if err != nil {
		return "", nil, fmt.Errorf("persist phenotype: %w", err)
	}
	desc := &NodeDescriptor{
		IdentityURN:   urn,
		GenotypeHash:  genotypeHash,
		PhenotypeHash: phenotypeHash,
		Semantics:     sem,
		Saliency:      saliency,
	}
	descHash, err := r.putDescriptor(desc)
	if err != nil {
		return "", nil, err
	}
	return descHash, desc, nil
}

// putDescriptor serializes and writes a descriptor block, returning its hash.
func (r *Repository) putDescriptor(desc *NodeDescriptor) (string, error) {
	payload, err := json.Marshal(desc)
	if err != nil {
		return "", fmt.Errorf("marshal descriptor: %w", err)
	}
	hash, err := r.ledger.WriteBlock(payload)
	if err != nil {
		return "", fmt.Errorf("persist descriptor: %w", err)
	}
	return hash, nil
}

// SeedRef makes a descriptor live by pointing the cell URN at it directly.
// Used at bootstrap before the evolutionary loop is running.
func (r *Repository) SeedRef(urn, descriptorHash string) error {
	return r.ledger.UpdateRef(urn, descriptorHash)
}

// Load resolves a cell URN to its active node descriptor.
func (r *Repository) Load(urn string) (*NodeDescriptor, error) {
	descHash, err := r.ledger.GetRef(urn)
	if err != nil {
		return nil, fmt.Errorf("resolve descriptor ref for %s: %w", urn, err)
	}
	return r.LoadHash(descHash)
}

// LoadHash reads a descriptor block by its content hash.
func (r *Repository) LoadHash(descriptorHash string) (*NodeDescriptor, error) {
	raw, err := r.ledger.ReadBlock(descriptorHash)
	if err != nil {
		return nil, fmt.Errorf("read descriptor block %s: %w", descriptorHash, err)
	}
	var desc NodeDescriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return nil, fmt.Errorf("decode descriptor %s: %w", descriptorHash, err)
	}
	return &desc, nil
}

// Genotype reads the WAT source block referenced by a descriptor.
func (r *Repository) Genotype(desc *NodeDescriptor) (string, error) {
	raw, err := r.ledger.ReadBlock(desc.GenotypeHash)
	if err != nil {
		return "", fmt.Errorf("read genotype %s: %w", desc.GenotypeHash, err)
	}
	return string(raw), nil
}

// Phenotype reads the compiled WASM block referenced by a descriptor.
func (r *Repository) Phenotype(desc *NodeDescriptor) ([]byte, error) {
	raw, err := r.ledger.ReadBlock(desc.PhenotypeHash)
	if err != nil {
		return nil, fmt.Errorf("read phenotype %s: %w", desc.PhenotypeHash, err)
	}
	return raw, nil
}
