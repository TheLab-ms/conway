package config

import (
	"database/sql"
	"fmt"
	"sort"
	"sync"
)

// Registry holds all registered configuration specs.
type Registry struct {
	mu    sync.RWMutex
	specs map[string]*ParsedSpec
	order []string // Track registration order
	db    *sql.DB
}

// NewRegistry creates a new configuration registry.
func NewRegistry(db *sql.DB) *Registry {
	return &Registry{
		specs: make(map[string]*ParsedSpec),
		db:    db,
	}
}

// Register adds a module's config spec to the registry.
// It parses the struct tags and validates the spec.
func (r *Registry) Register(spec Spec) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if spec.Module == "" {
		return fmt.Errorf("spec.Module is required")
	}
	if _, exists := r.specs[spec.Module]; exists {
		return fmt.Errorf("module %q already registered", spec.Module)
	}

	// Parse the spec
	parsed, err := parseSpec(spec)
	if err != nil {
		return fmt.Errorf("parsing spec for %s: %w", spec.Module, err)
	}

	r.specs[spec.Module] = parsed
	r.order = append(r.order, spec.Module)

	return nil
}

// MustRegister registers a spec and panics on error.
func (r *Registry) MustRegister(spec Spec) {
	if err := r.Register(spec); err != nil {
		panic(fmt.Errorf("failed to register config spec: %w", err))
	}
}

// Get returns a module's parsed config spec.
func (r *Registry) Get(module string) (*ParsedSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.specs[module]
	return spec, ok
}

// List returns all registered specs sorted by Order then Title.
func (r *Registry) List() []*ParsedSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()

	specs := make([]*ParsedSpec, 0, len(r.specs))
	for _, spec := range r.specs {
		specs = append(specs, spec)
	}

	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Order != specs[j].Order {
			return specs[i].Order < specs[j].Order
		}
		return specs[i].Title < specs[j].Title
	})

	return specs
}

// Modules returns all registered module names in registration order.
func (r *Registry) Modules() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, len(r.order))
	copy(result, r.order)
	return result
}
