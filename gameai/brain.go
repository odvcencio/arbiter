package gameai

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
)

// Spec names one decision a Brain makes: the tag that selects its rules and
// how it turns matches into a choice. Name defaults to Tag.
type Spec struct {
	Name   string
	Tag    string
	Select Selection
}

// Option configures a Brain.
type Option func(*brainOptions)

type brainOptions struct {
	allowMissing bool
}

// AllowMissing lets a Brain run rules that read variable names the view type
// does not provide; they read as null. By default NewBrain and Swap reject
// such a program, because a misspelt field otherwise makes a rule silently
// never fire.
func AllowMissing() Option { return func(o *brainOptions) { o.allowMissing = true } }

// Brain makes a fixed set of decisions for agents described by view type T.
// It holds one Program at a time and swaps it atomically (see Swap), so a
// game can hot-reload AI between ticks.
//
// A Brain is meant for one simulation goroutine: Decide and Explain must not
// run concurrently with each other. Swap may run on another goroutine. Give
// each room or match its own Brain; they can share one Program.
type Brain[T any] struct {
	specs []Spec
	opts  brainOptions
	state atomic.Pointer[brainState[T]]
}

type brainState[T any] struct {
	prog     *Program
	ctx      *Struct[T]
	deciders []*Decider
}

// NewBrain prepares the decisions in specs against p.
func NewBrain[T any](p *Program, specs []Spec, opts ...Option) (*Brain[T], error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("gameai: brain needs at least one decision")
	}
	b := &Brain[T]{specs: make([]Spec, len(specs))}
	seen := map[string]bool{}
	for i, s := range specs {
		if s.Name == "" {
			s.Name = s.Tag
		}
		if s.Name == "" {
			return nil, fmt.Errorf("gameai: decision %d has no name or tag", i)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("gameai: duplicate decision %q", s.Name)
		}
		seen[s.Name] = true
		b.specs[i] = s
	}
	for _, o := range opts {
		o(&b.opts)
	}
	st, err := b.build(p)
	if err != nil {
		return nil, err
	}
	b.state.Store(st)
	return b, nil
}

func (b *Brain[T]) build(p *Program) (*brainState[T], error) {
	ctx, err := Bind[T](p)
	if err != nil {
		return nil, err
	}
	st := &brainState[T]{prog: p, ctx: ctx, deciders: make([]*Decider, len(b.specs))}
	var missing []string
	for i, s := range b.specs {
		d, err := NewDecider(p, s.Tag)
		if err != nil {
			return nil, fmt.Errorf("gameai: decision %q: %w", s.Name, err)
		}
		st.deciders[i] = d
		if b.opts.allowMissing {
			continue
		}
		for _, name := range d.Reads() {
			if ok, typ := ctx.provides(name); !ok {
				if typ != "" {
					name += " (field type " + typ + " is not supported)"
				}
				missing = append(missing, s.Name+": "+name)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		var zero T
		return nil, fmt.Errorf("gameai: %T does not provide variables the rules read: %s", zero, strings.Join(missing, ", "))
	}
	return st, nil
}

// Program returns the program in use.
func (b *Brain[T]) Program() *Program { return b.state.Load().prog }

// Swap replaces the program. It prepares every decision against p first and
// returns an error, keeping the old program, if p lacks a decision's tag or
// reads variables T does not provide. Evaluations already running finish on
// the old program.
//
// Peers in a networked match must swap at the same tick: call Swap between
// ticks, at a tick the server announces, and record Program().Digest() with
// snapshots so a rollback past the swap restores the right program.
func (b *Brain[T]) Swap(p *Program) error {
	if p == nil {
		return fmt.Errorf("gameai: swap: nil program")
	}
	st, err := b.build(p)
	if err != nil {
		return err
	}
	b.state.Store(st)
	return nil
}

// Decision returns a handle for the named decision. Handles stay valid across
// Swap.
func (b *Brain[T]) Decision(name string) (Decision[T], error) {
	for i, s := range b.specs {
		if s.Name == name {
			return Decision[T]{b: b, slot: i}, nil
		}
	}
	return Decision[T]{}, fmt.Errorf("gameai: no decision %q", name)
}

// Decision is a handle for one of a Brain's decisions.
type Decision[T any] struct {
	b    *Brain[T]
	slot int
}

// Name returns the decision's name.
func (h Decision[T]) Name() string { return h.b.specs[h.slot].Name }

// Decide makes the decision for the agent described by view. seed matters only
// for Weighted decisions; derive it with Seed. The Choice is valid until the
// next Decide or Explain of the same decision.
func (h Decision[T]) Decide(view *T, seed uint64) (Choice, error) {
	return h.Explain(view, seed, nil)
}

// Explain is Decide that also fills tr, for a debug overlay. With a nil tr it
// is Decide. Reuse one Trace per watched agent; it does not allocate once its
// buffers have grown.
func (h Decision[T]) Explain(view *T, seed uint64, tr *Trace) (Choice, error) {
	st := h.b.state.Load()
	spec := &h.b.specs[h.slot]
	c, err := st.deciders[h.slot].Decide(st.ctx.Set(view), spec.Select, seed, tr)
	st.ctx.Set(nil)
	if tr != nil {
		tr.Decision = spec.Name
	}
	return c, err
}
