package grpcserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	arbiter "github.com/odvcencio/arbiter"
	"github.com/odvcencio/arbiter/expert"
	"github.com/odvcencio/arbiter/flags"
)

// Bundle is a published governed artifact available over gRPC.
type Bundle struct {
	ID              string
	Name            string
	Checksum        string
	Source          []byte
	Published       time.Time
	Compiled        *arbiter.CompileResult
	Expert          *expert.Program
	Flags           *flags.Flags
	RuleCount       int
	ExpertRuleCount int
	FlagCount       int
}

// Registry stores published bundles in memory.
type Registry struct {
	mu      sync.RWMutex
	bundles map[string]*Bundle
}

// NewRegistry creates an empty in-memory bundle registry.
func NewRegistry() *Registry {
	return &Registry{bundles: make(map[string]*Bundle)}
}

// Publish compiles and stores a governed bundle.
func (r *Registry) Publish(name string, source []byte) (*Bundle, error) {
	sum := sha256.Sum256(source)
	checksum := hex.EncodeToString(sum[:])
	id := checksum[:16]

	r.mu.RLock()
	if existing, ok := r.bundles[id]; ok {
		r.mu.RUnlock()
		return existing, nil
	}
	r.mu.RUnlock()

	parsed, err := arbiter.ParseSource(source)
	if err != nil {
		return nil, fmt.Errorf("parse bundle: %w", err)
	}
	compiled, err := arbiter.CompileFullParsed(parsed)
	if err != nil {
		return nil, fmt.Errorf("compile rules: %w", err)
	}
	flagSet, err := flags.LoadParsed(parsed, compiled)
	if err != nil {
		return nil, fmt.Errorf("compile flags: %w", err)
	}
	expertProgram, err := expert.CompileParsed(parsed, compiled)
	if err != nil {
		return nil, fmt.Errorf("compile expert rules: %w", err)
	}

	bundle := &Bundle{
		ID:              id,
		Name:            name,
		Checksum:        checksum,
		Source:          append([]byte(nil), source...),
		Published:       time.Now().UTC(),
		Compiled:        compiled,
		Expert:          expertProgram,
		Flags:           flagSet,
		RuleCount:       len(compiled.Ruleset.Rules),
		ExpertRuleCount: len(expertProgram.Rules()),
		FlagCount:       flagSet.Count(),
	}

	r.mu.Lock()
	if existing, ok := r.bundles[id]; ok {
		r.mu.Unlock()
		return existing, nil
	}
	r.bundles[id] = bundle
	r.mu.Unlock()
	return bundle, nil
}

// Get returns a previously published bundle.
func (r *Registry) Get(id string) (*Bundle, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	bundle, ok := r.bundles[id]
	return bundle, ok
}
