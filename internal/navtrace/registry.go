package navtrace

import "sync"

// ID returns the 32-bit tracking id for a skill name.
//
// It is derived, not assigned. Registration order changes when skills are
// split, merged or renamed, so a counter would give the same skill a different
// id in the next generation and cross-generation traces would stop joining. A
// hash of the canonical name costs one pass and agrees across processes,
// restarts and generations with no registry file to seed.
//
// Zero is reserved for "no skill / ambient": a step that only records a flow
// effect has no identity to track.
func ID(name string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 16777619
	}
	if h == 0 {
		return 1
	}
	return h
}

// Registry resolves the ids that appear in a trace back to names, and records
// lineage when a refinement renames a skill.
//
// The registry is not needed to produce an id — ID is pure. It exists for the
// reader side: a trace from an old generation names an id that no longer maps
// to a live tool, and Alias is how that continuity is declared instead of
// guessed.
type Registry struct {
	mu    sync.Mutex
	byID  map[uint32]string
	alias map[string]string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byID:  make(map[uint32]string),
		alias: make(map[string]string),
	}
}

// Register binds name to its derived id and reports a hash collision with a
// different name. Collisions are caught here, at registration, rather than in
// analysis after a trace has already been read.
func (r *Registry) Register(name string) (uint32, error) {
	id := ID(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byID[id]; ok && existing != name {
		return 0, &CollisionError{ID: id, A: existing, B: name}
	}
	r.byID[id] = name
	return id, nil
}

// Alias declares that name continues the identity of an earlier name, so a
// trace recorded under either resolves to the same lineage.
func (r *Registry) Alias(name, earlier string) error {
	if name == "" || earlier == "" {
		return &AliasError{Name: name, Earlier: earlier}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := ID(earlier)
	if existing, ok := r.byID[id]; ok && existing != earlier {
		return &CollisionError{ID: id, A: existing, B: earlier}
	}
	r.byID[id] = earlier
	r.alias[name] = earlier
	return nil
}

// ID returns the id for name, following an alias if one was declared.
func (r *Registry) ID(name string) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		earlier, ok := r.alias[name]
		if !ok || earlier == name {
			break
		}
		name = earlier
	}
	return ID(name)
}

// Resolve maps an id back to the canonical name.
func (r *Registry) Resolve(id uint32) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.byID[id]
	return name, ok
}

// CollisionError reports two names hashing to the same 32-bit id.
type CollisionError struct {
	ID uint32
	A  string
	B  string
}

func (e *CollisionError) Error() string {
	return "navtrace: skill id collision " + e.A + " / " + e.B
}

// AliasError reports an empty name in an alias declaration.
type AliasError struct {
	Name    string
	Earlier string
}

func (e *AliasError) Error() string { return "navtrace: alias needs two names" }
