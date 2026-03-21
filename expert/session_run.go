package expert

import (
	"context"
	"fmt"

	"github.com/odvcencio/arbiter/vm"
)

// Run evaluates the expert program until it reaches a fixed point or a guardrail.
func (s *Session) Run(ctx context.Context) (Result, error) {
	if err := s.validateRunState(); err != nil {
		return Result{}, err
	}
	if s.hasNoRunnableRules() {
		s.stopReason = StopQuiescent
		return s.snapshot(), nil
	}
	forceFullEval := s.prepareRun()
	if s.shouldReuseQuiescentSnapshot(forceFullEval) {
		return s.snapshot(), nil
	}

	for round := 1; round <= s.opts.MaxRounds; round++ {
		stopped, cancelled, err := s.runLoopRound(ctx, round, forceFullEval)
		if err != nil {
			if cancelled {
				return s.snapshot(), err
			}
			return Result{}, err
		}
		if stopped {
			return s.snapshot(), nil
		}
	}

	s.stopReason = StopMaxRounds
	s.contextDirty = false
	return s.snapshot(), nil
}

func (s *Session) validateRunState() error {
	if s == nil || s.program == nil {
		return fmt.Errorf("nil expert program")
	}
	return nil
}

func (s *Session) hasNoRunnableRules() bool {
	return s.program.ruleset == nil || len(s.program.rules) == 0
}

func (s *Session) prepareRun() bool {
	nextNow := s.currentUnix()
	forceFullEval := nextNow != s.evalNow || s.contextDirty
	s.evalNow = nextNow
	return forceFullEval
}

func (s *Session) shouldReuseQuiescentSnapshot(forceFullEval bool) bool {
	return s.rounds > 0 && s.stopReason == StopQuiescent && len(s.dirtyFacts) == 0 && !forceFullEval
}

func (s *Session) runLoopRound(ctx context.Context, round int, forceFullEval bool) (bool, bool, error) {
	if err := ctx.Err(); err != nil {
		s.stopReason = StopContextCancelled
		return true, true, err
	}

	s.rounds++
	roundMutationsStart := s.mutations
	forceStableRound := s.stablePending && s.rounds > 1 && s.lastRoundMutations == 0
	firedGroups := make(map[string]struct{})
	firstPass := s.rounds == 1 || (forceFullEval && round == 1)

	roundResult, err := s.executeRound(ctx, round, firstPass, firedGroups)
	if err != nil {
		return false, false, err
	}
	if roundResult.stopped {
		return true, false, nil
	}

	s.lastRoundMutations = s.mutations - roundMutationsStart
	if s.shouldStopAfterRound(roundResult, forceStableRound) {
		return true, false, nil
	}
	return false, false, nil
}

func (s *Session) executeRound(ctx context.Context, round int, firstPass bool, firedGroups map[string]struct{}) (roundExecution, error) {
	dirtyFacts := s.copyDirtyFacts()
	dirtySources := s.copyDirtySources()
	s.clearDirtyFacts()

	matched, ruleChanges, evaluated, stableDeferred, err := s.runRound(firstPass, dirtyFacts, dirtySources)
	if err != nil {
		return roundExecution{}, err
	}

	mutated, active, stopped, err := s.applyRoundMatches(ctx, round, matched, firedGroups)
	if err != nil {
		return roundExecution{}, err
	}
	mutated = s.clearInactiveEvaluatedMutations(evaluated, active) || mutated

	return roundExecution{
		mutated:        mutated,
		stopped:        stopped,
		stableDeferred: stableDeferred,
		ruleChanges:    ruleChanges,
	}, nil
}

func (s *Session) shouldStopAfterRound(result roundExecution, forceStableRound bool) bool {
	if !result.mutated {
		if result.stableDeferred {
			return false
		}
		if forceStableRound {
			s.stablePending = false
		}
		s.contextDirty = false
		s.stopReason = StopQuiescent
		return true
	}
	if len(result.ruleChanges) == 0 && len(s.dirtyFacts) == 0 {
		s.stopReason = StopQuiescent
		return true
	}
	if !s.hasPendingWork(result.ruleChanges) {
		s.stopReason = StopQuiescent
		return true
	}
	return false
}

func (s *Session) applyRoundMatches(ctx context.Context, round int, matched []vm.MatchedRule, firedGroups map[string]struct{}) (bool, activeRoundMutations, bool, error) {
	active := activeRoundMutations{
		asserts:  make(map[string]struct{}),
		retracts: make(map[string]struct{}),
		modifies: make(map[string]struct{}),
	}
	mutated := false

	for _, match := range matched {
		if err := ctx.Err(); err != nil {
			s.stopReason = StopContextCancelled
			return false, active, true, err
		}

		rule, ok := s.program.lookupRule(match.Name)
		if !ok {
			return false, active, false, fmt.Errorf("missing expert rule metadata for %q", match.Name)
		}
		if s.groupAlreadyFired(rule, firedGroups) {
			continue
		}

		changed, err := s.applyMatchedRule(round, rule, match, active)
		if err != nil {
			return false, active, false, err
		}
		mutated = mutated || changed
		if rule.Group != "" {
			firedGroups[rule.Group] = struct{}{}
		}
		if s.mutations >= s.opts.MaxMutations {
			s.stopReason = StopMaxMutations
			return mutated, active, true, nil
		}
	}

	return mutated, active, false, nil
}

func (s *Session) groupAlreadyFired(rule Rule, firedGroups map[string]struct{}) bool {
	if rule.Group == "" {
		return false
	}
	_, blocked := firedGroups[rule.Group]
	return blocked
}

func (s *Session) applyMatchedRule(round int, rule Rule, match vm.MatchedRule, active activeRoundMutations) (bool, error) {
	switch rule.Kind {
	case ActionAssert:
		changed, _, instance, err := s.applyAssert(round, rule, match)
		if err != nil {
			return false, err
		}
		active.asserts[instance] = struct{}{}
		return changed, nil
	case ActionEmit:
		_, err := s.applyEmit(round, rule, match)
		return false, err
	case ActionRetract:
		changed, _, instance, err := s.applyRetract(round, rule, match)
		if err != nil {
			return false, err
		}
		active.retracts[instance] = struct{}{}
		return changed, nil
	case ActionModify:
		changed, _, instance, err := s.applyModify(round, rule, match)
		if err != nil {
			return false, err
		}
		active.modifies[instance] = struct{}{}
		return changed, nil
	default:
		return false, fmt.Errorf("unsupported expert action kind %q", rule.Kind)
	}
}

func (s *Session) clearInactiveEvaluatedMutations(evaluated map[string]struct{}, active activeRoundMutations) bool {
	mutated := false
	for ruleName := range evaluated {
		rule, ok := s.program.lookupRule(ruleName)
		if !ok {
			continue
		}
		switch rule.Kind {
		case ActionAssert:
			if s.clearInactiveDerivedSupports(ruleName, active.asserts) {
				mutated = true
			}
		case ActionRetract:
			if s.clearInactiveRetractions(ruleName, active.retracts) {
				mutated = true
			}
		case ActionModify:
			if s.clearInactiveModifications(ruleName, active.modifies) {
				mutated = true
			}
		}
	}
	return mutated
}
