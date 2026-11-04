package grpcserver

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	arbiter "m31labs.dev/arbiter"
	arbiterv1 "m31labs.dev/arbiter/api/arbiter/v1"
	"m31labs.dev/arbiter/audit"
	"m31labs.dev/arbiter/flags"
	"m31labs.dev/arbiter/govern"
	"m31labs.dev/arbiter/ir"
	"m31labs.dev/arbiter/observability"
	"m31labs.dev/arbiter/overrides"
	"m31labs.dev/arbiter/vm"
)

// Server implements the Arbiter gRPC API.
type Server struct {
	arbiterv1.UnimplementedArbiterServiceServer
	registry  *Registry
	sessions  *SessionStore
	overrides *overrides.Store
	audit     audit.Sink
	logger    *slog.Logger
}

// NewServer creates a gRPC service server.
func NewServer(registry *Registry, store *overrides.Store, sink audit.Sink) *Server {
	return NewServerWithLoggerAndSessions(registry, store, sink, nil, nil)
}

// NewServerWithLogger creates a gRPC service server with a custom structured logger.
func NewServerWithLogger(registry *Registry, store *overrides.Store, sink audit.Sink, logger *slog.Logger) *Server {
	return NewServerWithLoggerAndSessions(registry, store, sink, logger, nil)
}

// NewServerWithLoggerAndSessions creates a gRPC service server with an optional
// custom structured logger and session store.
func NewServerWithLoggerAndSessions(registry *Registry, store *overrides.Store, sink audit.Sink, logger *slog.Logger, sessions *SessionStore) *Server {
	if registry == nil {
		registry = NewRegistry()
	}
	if store == nil {
		store = overrides.NewStore()
	}
	if sink == nil {
		sink = audit.NopSink{}
	}
	if logger == nil {
		logger = observability.NewLogger(slog.LevelInfo)
	}
	if sessions == nil {
		sessions = NewSessionStore()
	}
	return &Server{
		registry:  registry,
		sessions:  sessions,
		overrides: store,
		audit:     sink,
		logger:    logger,
	}
}

// PublishBundle compiles and stores a governed bundle.
func (s *Server) PublishBundle(ctx context.Context, req *arbiterv1.PublishBundleRequest) (*arbiterv1.PublishBundleResponse, error) {
	if len(req.GetSource()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "source is required")
	}
	bundle, err := s.registry.Publish(req.GetName(), req.GetSource())
	if err != nil {
		s.logger.Error("bundle publish failed",
			observability.KeyBundleName, req.GetName(),
			observability.KeyError, err.Error())
		return nil, status.Errorf(codes.InvalidArgument, "publish bundle: %v", err)
	}
	bundlePublishTotal.Inc()
	s.logger.Info("bundle published",
		observability.KeyBundleName, bundle.Name,
		observability.KeyBundleID, bundle.ID)
	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		BundleID:  bundle.ID,
		Kind:      "bundle",
		Bundle: &audit.BundleChange{
			Action:   "publish",
			Name:     bundle.Name,
			BundleID: bundle.ID,
			Checksum: bundle.Checksum,
		},
	})
	return &arbiterv1.PublishBundleResponse{
		BundleId:        bundle.ID,
		Checksum:        bundle.Checksum,
		RuleCount:       uint32(bundle.RuleCount),
		FlagCount:       uint32(bundle.FlagCount),
		ExpertRuleCount: uint32(bundle.ExpertRuleCount),
		StrategyCount:   uint32(bundle.StrategyCount),
		PublishedAt:     timestamppb.New(bundle.Published),
	}, nil
}

// ListBundles lists published bundles and active versions.
func (s *Server) ListBundles(_ context.Context, req *arbiterv1.ListBundlesRequest) (*arbiterv1.ListBundlesResponse, error) {
	bundles := s.registry.List(req.GetName())
	resp := &arbiterv1.ListBundlesResponse{
		Bundles: make([]*arbiterv1.BundleSummary, 0, len(bundles)),
	}
	for _, bundle := range bundles {
		resp.Bundles = append(resp.Bundles, s.protoBundleSummary(bundle))
	}
	return resp, nil
}

// ActivateBundle switches the active version for one bundle name.
func (s *Server) ActivateBundle(ctx context.Context, req *arbiterv1.ActivateBundleRequest) (*arbiterv1.ActivateBundleResponse, error) {
	if req.GetName() == "" || req.GetBundleId() == "" {
		return nil, status.Error(codes.InvalidArgument, "name and bundle_id are required")
	}
	bundle, err := s.registry.Activate(req.GetName(), req.GetBundleId())
	if err != nil {
		s.logger.Error("bundle activate failed",
			observability.KeyBundleName, req.GetName(),
			observability.KeyBundleID, req.GetBundleId(),
			observability.KeyError, err.Error())
		return nil, status.Errorf(codes.InvalidArgument, "activate bundle: %v", err)
	}
	s.logger.Info("bundle activated",
		observability.KeyBundleName, bundle.Name,
		observability.KeyBundleID, bundle.ID)
	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		BundleID:  bundle.ID,
		Kind:      "bundle",
		Bundle: &audit.BundleChange{
			Action:   "activate",
			Name:     bundle.Name,
			BundleID: bundle.ID,
			Checksum: bundle.Checksum,
		},
	})
	return &arbiterv1.ActivateBundleResponse{Bundle: s.protoBundleSummary(bundle)}, nil
}

// RollbackBundle reactivates the previous published bundle version.
func (s *Server) RollbackBundle(ctx context.Context, req *arbiterv1.RollbackBundleRequest) (*arbiterv1.RollbackBundleResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	bundle, previous, err := s.registry.Rollback(req.GetName())
	if err != nil {
		s.logger.Error("bundle rollback failed",
			observability.KeyBundleName, req.GetName(),
			observability.KeyError, err.Error())
		return nil, status.Errorf(codes.InvalidArgument, "rollback bundle: %v", err)
	}
	s.logger.Info("bundle rolled back",
		observability.KeyBundleName, bundle.Name,
		observability.KeyBundleID, bundle.ID)
	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		BundleID:  bundle.ID,
		Kind:      "bundle",
		Bundle: &audit.BundleChange{
			Action:           "rollback",
			Name:             bundle.Name,
			BundleID:         bundle.ID,
			Checksum:         bundle.Checksum,
			PreviousBundleID: previous.ID,
		},
	})
	return &arbiterv1.RollbackBundleResponse{
		Bundle:           s.protoBundleSummary(bundle),
		PreviousBundleId: previous.ID,
	}, nil
}

// GetBundle fetches a published bundle and returns its raw source.
func (s *Server) GetBundle(_ context.Context, req *arbiterv1.GetBundleRequest) (*arbiterv1.GetBundleResponse, error) {
	bundle, err := s.bundleRef(req.GetBundleId(), req.GetBundleName())
	if err != nil {
		return nil, err
	}
	return &arbiterv1.GetBundleResponse{
		Bundle: s.protoBundleSummary(bundle),
		Source: append([]byte(nil), bundle.Source...),
	}, nil
}

// GetOverrides fetches all runtime overrides for one bundle.
func (s *Server) GetOverrides(_ context.Context, req *arbiterv1.GetOverridesRequest) (*arbiterv1.GetOverridesResponse, error) {
	bundle, err := s.bundleRef(req.GetBundleId(), req.GetBundleName())
	if err != nil {
		return nil, err
	}
	return &arbiterv1.GetOverridesResponse{
		Overrides: s.protoBundleOverrides(s.overrides.SnapshotForBundle(bundle.ID)),
	}, nil
}

// WatchOverrides streams one bundle's override snapshot and subsequent mutations.
func (s *Server) WatchOverrides(req *arbiterv1.WatchOverridesRequest, stream arbiterv1.ArbiterService_WatchOverridesServer) error {
	if req.GetBundleId() == "" {
		return status.Error(codes.InvalidArgument, "bundle_id is required")
	}
	if _, err := s.bundle(req.GetBundleId()); err != nil {
		return err
	}

	snapshot, events, cancel := s.overrides.Subscribe(req.GetBundleId())
	defer cancel()

	if err := stream.Send(s.protoOverrideEvent(overrides.OverrideEvent{
		Type:     overrides.OverrideEventSnapshot,
		BundleID: snapshot.BundleID,
		Snapshot: snapshot,
	})); err != nil {
		return err
	}
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case event := <-events:
			if err := stream.Send(s.protoOverrideEvent(event)); err != nil {
				return err
			}
		}
	}
}

// WatchBundles streams the active bundle state and subsequent updates.
func (s *Server) WatchBundles(req *arbiterv1.WatchBundlesRequest, stream arbiterv1.ArbiterService_WatchBundlesServer) error {
	ctx := stream.Context()
	nameFilter := uniqueNames(req.GetNames())

	initial, events, cancel := s.registry.SubscribeBundles(req.GetNames(), req.GetActiveOnly())
	defer cancel()

	for _, bundle := range initial {
		if err := stream.Send(s.protoBundleEvent(bundleEvent{
			Type:   bundleEventSnapshot,
			Bundle: bundle,
		})); err != nil {
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-events:
			if event.Bundle == nil {
				continue
			}
			if len(nameFilter) > 0 {
				if _, ok := nameFilter[event.Bundle.Name]; !ok {
					continue
				}
			}
			if err := stream.Send(s.protoBundleEvent(event)); err != nil {
				return err
			}
		}
	}
}

func (s *Server) captureDecision(facts *structpb.Struct, at time.Time, bundleID string) (govern.DecisionSnapshot, error) {
	decision, err := govern.NewDecisionSnapshot(facts.AsMap(), at, bundleID, s.overrides)
	if err != nil {
		return govern.DecisionSnapshot{}, status.Errorf(codes.InvalidArgument, "decision context: %v", err)
	}
	return decision, nil
}

// EvaluateRules evaluates rules in a published bundle.
func (s *Server) EvaluateRules(ctx context.Context, req *arbiterv1.EvaluateRulesRequest) (*arbiterv1.EvaluateRulesResponse, error) {
	bundle, err := s.bundleRef(req.GetBundleId(), req.GetBundleName())
	if err != nil {
		return nil, err
	}
	if bundle.RuleCount == 0 {
		return nil, status.Error(codes.FailedPrecondition, "bundle has no rules")
	}

	ctx, span := startEvalSpan(ctx, "arbiter.eval.governed", bundle.Name)
	var matched []vm.MatchedRule
	defer func() { endEvalSpan(span, len(matched), err) }()

	start := time.Now()
	decision, err := s.captureDecision(req.GetContext(), start, bundle.ID)
	if err != nil {
		return nil, err
	}
	prog := &arbiter.Program{Ruleset: bundle.Compiled.Ruleset, Segments: bundle.Compiled.Segments}
	matched, trace, err := arbiter.EvalDecision(prog, decision)
	elapsed := time.Since(start)
	evalStatus := "ok"
	if err != nil {
		evalStatus = "error"
		evalTotal.WithLabelValues(bundle.Name, "rules", evalStatus).Inc()
		evalDuration.WithLabelValues(bundle.Name, "rules", evalStatus).Observe(elapsed.Seconds())
		s.logger.Error("evaluate rules failed",
			observability.KeyBundleName, bundle.Name,
			observability.KeyBundleID, bundle.ID,
			observability.KeyRequestID, req.GetRequestId(),
			observability.KeyError, err.Error())
		return nil, status.Errorf(codes.Internal, "evaluate rules: %v", err)
	}
	evalTotal.WithLabelValues(bundle.Name, "rules", evalStatus).Inc()
	evalDuration.WithLabelValues(bundle.Name, "rules", evalStatus).Observe(elapsed.Seconds())
	ruleMatchesTotal.WithLabelValues(bundle.Name).Add(float64(len(matched)))

	resp := &arbiterv1.EvaluateRulesResponse{
		Matched:   make([]*arbiterv1.RuleMatch, 0, len(matched)),
		Arbitrace: protoArbitrace(trace.Steps),
	}
	for _, m := range matched {
		params, err := structpb.NewStruct(cleanMap(m.Params))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "marshal params: %v", err)
		}
		resp.Matched = append(resp.Matched, &arbiterv1.RuleMatch{
			Name:     m.Name,
			Priority: int32(m.Priority),
			Action:   m.Action,
			Params:   params,
			Fallback: m.Fallback,
		})
	}

	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: decision.EvalTime(),
		RequestID: req.GetRequestId(),
		BundleID:  bundle.ID,
		Kind:      "rules",
		Context:   decision.Facts(),
		Rules:     auditRuleMatches(matched),
		Arbitrace: trace.Steps,
	})
	return resp, nil
}

// ResolveFlag evaluates a flag in a published bundle.
func (s *Server) ResolveFlag(ctx context.Context, req *arbiterv1.ResolveFlagRequest) (*arbiterv1.ResolveFlagResponse, error) {
	bundle, err := s.bundleRef(req.GetBundleId(), req.GetBundleName())
	if err != nil {
		return nil, err
	}
	if !bundle.Flags.Has(req.GetFlagKey()) {
		return nil, status.Errorf(codes.NotFound, "flag %q not found", req.GetFlagKey())
	}

	ctx, span := startEvalSpan(ctx, "arbiter.flag.resolve", bundle.Name)
	span.SetAttributes(attribute.String("arbiter.flag.name", req.GetFlagKey()))
	var variant string
	defer func() {
		span.SetAttributes(attribute.String("arbiter.flag.variant", variant))
		span.End()
	}()

	start := time.Now()
	decision, err := s.captureDecision(req.GetContext(), start, bundle.ID)
	if err != nil {
		return nil, err
	}
	eval := bundle.Flags.ExplainDecision(req.GetFlagKey(), decision)
	elapsed := time.Since(start)
	evalTotal.WithLabelValues(bundle.Name, "governed", "ok").Inc()
	evalDuration.WithLabelValues(bundle.Name, "governed", "ok").Observe(elapsed.Seconds())
	flagResolvesTotal.WithLabelValues(bundle.Name, req.GetFlagKey()).Inc()

	values, err := structpb.NewStruct(cleanMap(eval.Variant.Values))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal values: %v", err)
	}

	env := bundle.Flags.Environment
	event := audit.DecisionEvent{
		Timestamp:   decision.EvalTime(),
		RequestID:   req.GetRequestId(),
		BundleID:    bundle.ID,
		Environment: env,
		Kind:        "flag",
		Context:     decision.Facts(),
		Flag: &audit.FlagDecision{
			Flag:        eval.Flag,
			Variant:     eval.Variant.Name,
			Values:      eval.Variant.Values,
			IsDefault:   eval.IsDefault,
			Reason:      eval.Reason,
			Environment: env,
		},
		Arbitrace: toGovernArbitrace(eval.Arbitrace),
	}

	// Emit assignment event for non-default resolutions (experimentation).
	if !eval.IsDefault {
		userID, _ := extractUserID(event.Context)
		event.Assignment = &audit.FlagAssignment{
			Flag:        eval.Flag,
			Variant:     eval.Variant.Name,
			UserID:      userID,
			Environment: env,
			Values:      eval.Variant.Values,
		}
	}

	_ = s.audit.WriteDecision(ctx, event)

	variant = eval.Variant.Name
	return &arbiterv1.ResolveFlagResponse{
		Variant:   eval.Variant.Name,
		Values:    values,
		IsDefault: eval.IsDefault,
		Reason:    eval.Reason,
		Arbitrace: protoArbitrace(eval.Arbitrace),
	}, nil
}

// EvaluateStrategy evaluates a strategy in a published bundle.
func (s *Server) EvaluateStrategy(ctx context.Context, req *arbiterv1.EvaluateStrategyRequest) (*arbiterv1.EvaluateStrategyResponse, error) {
	bundle, err := s.bundleRef(req.GetBundleId(), req.GetBundleName())
	if err != nil {
		return nil, err
	}
	if bundle.Compiled == nil || bundle.Compiled.Strategies == nil || bundle.StrategyCount == 0 {
		return nil, status.Error(codes.FailedPrecondition, "bundle has no strategies")
	}
	if !bundle.Compiled.Strategies.Has(req.GetStrategyName()) {
		return nil, status.Errorf(codes.NotFound, "strategy %q not found", req.GetStrategyName())
	}

	ctx, span := startEvalSpan(ctx, "arbiter.eval.strategy", bundle.Name)
	span.SetAttributes(attribute.String("arbiter.strategy.name", req.GetStrategyName()))
	var selectedLabel string
	defer func() {
		span.SetAttributes(attribute.String("arbiter.strategy.selected", selectedLabel))
		span.End()
	}()

	start := time.Now()
	decision, err := s.captureDecision(req.GetContext(), start, bundle.ID)
	if err != nil {
		return nil, err
	}
	result, err := bundle.Compiled.Strategies.EvaluateDecision(req.GetStrategyName(), decision)
	elapsed := time.Since(start)
	evalStatus := "ok"
	if err != nil {
		evalStatus = "error"
		evalTotal.WithLabelValues(bundle.Name, "strategy", evalStatus).Inc()
		evalDuration.WithLabelValues(bundle.Name, "strategy", evalStatus).Observe(elapsed.Seconds())
		return nil, status.Errorf(codes.Internal, "evaluate strategy: %v", err)
	}
	evalTotal.WithLabelValues(bundle.Name, "strategy", evalStatus).Inc()
	evalDuration.WithLabelValues(bundle.Name, "strategy", evalStatus).Observe(elapsed.Seconds())

	params, err := structpb.NewStruct(cleanMap(result.Params))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal params: %v", err)
	}

	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: decision.EvalTime(),
		RequestID: req.GetRequestId(),
		BundleID:  bundle.ID,
		Kind:      "strategy",
		Context:   decision.Facts(),
		Strategy: &audit.StrategyDecision{
			Strategy: result.Strategy,
			Outcome:  result.Outcome,
			Selected: result.Selected,
			Params:   result.Params,
		},
		Arbitrace: result.Arbitrace.Steps,
	})

	selectedLabel = result.Selected
	return &arbiterv1.EvaluateStrategyResponse{
		Outcome:   result.Outcome,
		Selected:  result.Selected,
		Params:    params,
		Arbitrace: protoArbitrace(result.Arbitrace.Steps),
	}, nil
}

// SetRuleOverride updates runtime rule overrides for a bundle.
func (s *Server) SetRuleOverride(ctx context.Context, req *arbiterv1.SetRuleOverrideRequest) (*arbiterv1.SetRuleOverrideResponse, error) {
	if _, err := s.bundle(req.GetBundleId()); err != nil {
		return nil, err
	}
	ov := overrides.RuleOverride{}
	if req.KillSwitch != nil {
		v := req.KillSwitch.GetValue()
		ov.KillSwitch = &v
	}
	if req.Rollout != nil {
		v, err := normalizeOverrideRollout(req.Rollout.GetValue())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		ov.Rollout = &v
	}
	if err := s.overrides.SetRule(req.GetBundleId(), req.GetRuleName(), ov); err != nil {
		return nil, status.Errorf(codes.Internal, "persist rule override: %v", err)
	}
	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		BundleID:  req.GetBundleId(),
		Kind:      "override",
		Override: &audit.OverrideChange{
			Scope:           "rule",
			Target:          req.GetRuleName(),
			KillSwitch:      ov.KillSwitch,
			KillSwitchState: string(ov.KillSwitchState()),
			Rollout:         ov.Rollout,
		},
	})
	return &arbiterv1.SetRuleOverrideResponse{}, nil
}

// SetFlagOverride updates runtime flag overrides for a bundle.
func (s *Server) SetFlagOverride(ctx context.Context, req *arbiterv1.SetFlagOverrideRequest) (*arbiterv1.SetFlagOverrideResponse, error) {
	bundle, err := s.bundle(req.GetBundleId())
	if err != nil {
		return nil, err
	}
	if !bundle.Flags.Has(req.GetFlagKey()) {
		return nil, status.Errorf(codes.NotFound, "flag %q not found", req.GetFlagKey())
	}
	ov := overrides.FlagOverride{}
	if req.KillSwitch != nil {
		v := req.KillSwitch.GetValue()
		ov.KillSwitch = &v
	}
	if err := s.overrides.SetFlag(req.GetBundleId(), req.GetFlagKey(), ov); err != nil {
		return nil, status.Errorf(codes.Internal, "persist flag override: %v", err)
	}
	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		BundleID:  req.GetBundleId(),
		Kind:      "override",
		Override: &audit.OverrideChange{
			Scope:           "flag",
			Target:          req.GetFlagKey(),
			KillSwitch:      ov.KillSwitch,
			KillSwitchState: string(ov.KillSwitchState()),
		},
	})
	return &arbiterv1.SetFlagOverrideResponse{}, nil
}

// SetFlagRuleOverride updates runtime rollout overrides for a flag rule.
func (s *Server) SetFlagRuleOverride(ctx context.Context, req *arbiterv1.SetFlagRuleOverrideRequest) (*arbiterv1.SetFlagRuleOverrideResponse, error) {
	bundle, err := s.bundle(req.GetBundleId())
	if err != nil {
		return nil, err
	}
	if !bundle.Flags.Has(req.GetFlagKey()) {
		return nil, status.Errorf(codes.NotFound, "flag %q not found", req.GetFlagKey())
	}
	if int(req.GetRuleIndex()) >= bundle.Flags.RuleCount(req.GetFlagKey()) {
		return nil, status.Error(codes.InvalidArgument, "rule_index out of range")
	}
	ov := overrides.FlagRuleOverride{}
	if req.Rollout != nil {
		v, err := normalizeOverrideRollout(req.Rollout.GetValue())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		ov.Rollout = &v
	}
	if err := s.overrides.SetFlagRule(req.GetBundleId(), req.GetFlagKey(), int(req.GetRuleIndex()), ov); err != nil {
		return nil, status.Errorf(codes.Internal, "persist flag rule override: %v", err)
	}
	ruleIndex := int(req.GetRuleIndex())
	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		BundleID:  req.GetBundleId(),
		Kind:      "override",
		Override: &audit.OverrideChange{
			Scope:     "flag_rule",
			Target:    req.GetFlagKey(),
			RuleIndex: &ruleIndex,
			Rollout:   ov.Rollout,
		},
	})
	return &arbiterv1.SetFlagRuleOverrideResponse{}, nil
}

// SetStrategyOverride updates runtime governance overrides for one strategy candidate.
func (s *Server) SetStrategyOverride(ctx context.Context, req *arbiterv1.SetStrategyOverrideRequest) (*arbiterv1.SetStrategyOverrideResponse, error) {
	bundle, err := s.bundle(req.GetBundleId())
	if err != nil {
		return nil, err
	}
	if bundle.Compiled == nil || bundle.Compiled.Strategies == nil || !bundle.Compiled.Strategies.Has(req.GetStrategyName()) {
		return nil, status.Errorf(codes.NotFound, "strategy %q not found", req.GetStrategyName())
	}
	if !bundle.Compiled.Strategies.HasCandidate(req.GetStrategyName(), req.GetCandidateLabel()) {
		return nil, status.Errorf(codes.NotFound, "strategy candidate %q not found in %q", req.GetCandidateLabel(), req.GetStrategyName())
	}

	ov := overrides.StrategyOverride{}
	if req.KillSwitch != nil {
		v := req.KillSwitch.GetValue()
		ov.KillSwitch = &v
	}
	if req.Rollout != nil {
		v, err := normalizeOverrideRollout(req.Rollout.GetValue())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		ov.Rollout = &v
	}

	if err := s.overrides.SetStrategy(req.GetBundleId(), req.GetStrategyName(), req.GetCandidateLabel(), ov); err != nil {
		return nil, status.Errorf(codes.Internal, "persist strategy override: %v", err)
	}

	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		BundleID:  req.GetBundleId(),
		Kind:      "override",
		Override: &audit.OverrideChange{
			Scope:           "strategy_candidate",
			Target:          req.GetStrategyName() + "/" + req.GetCandidateLabel(),
			KillSwitch:      ov.KillSwitch,
			KillSwitchState: string(ov.KillSwitchState()),
			Rollout:         ov.Rollout,
		},
	})
	return &arbiterv1.SetStrategyOverrideResponse{}, nil
}

func (s *Server) bundle(id string) (*Bundle, error) {
	bundle, ok := s.registry.Get(id)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "bundle %q not found", id)
	}
	return bundle, nil
}

func (s *Server) bundleRef(id, name string) (*Bundle, error) {
	bundle, err := s.registry.Resolve(id, name)
	if err != nil {
		if id == "" && name == "" {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return bundle, nil
}

func auditRuleMatches(matched []vm.MatchedRule) []audit.RuleMatch {
	out := make([]audit.RuleMatch, 0, len(matched))
	for _, m := range matched {
		out = append(out, audit.RuleMatch{
			Name:     m.Name,
			Priority: m.Priority,
			Action:   m.Action,
			Params:   m.Params,
			Fallback: m.Fallback,
		})
	}
	return out
}

func cleanMap(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cleanValue(v)
	}
	return out
}

func cleanValue(v any) any {
	switch val := v.(type) {
	case nil:
		return nil
	case map[string]any:
		return cleanMap(val)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = cleanValue(item)
		}
		return out
	case []string:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = item
		}
		return out
	case []int:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = item
		}
		return out
	case []float64:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = item
		}
		return out
	case []bool:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = item
		}
		return out
	default:
		return val
	}
}

// extractUserID looks for a user identifier in the evaluation context.
// Checks common locations: user_id, user.id, userId.
func extractUserID(ctx map[string]any) (string, bool) {
	// Top-level user_id
	if id, ok := ctx["user_id"].(string); ok && id != "" {
		return id, true
	}
	// Nested user.id
	if user, ok := ctx["user"].(map[string]any); ok {
		if id, ok := user["id"].(string); ok && id != "" {
			return id, true
		}
	}
	return "", false
}

func protoArbitrace(steps []govern.ArbitraceStep) []*arbiterv1.ArbitraceStep {
	out := make([]*arbiterv1.ArbitraceStep, 0, len(steps))
	for _, step := range steps {
		out = append(out, &arbiterv1.ArbitraceStep{
			Check:       step.Check,
			Result:      step.Result,
			Detail:      step.Detail,
			Phase:       step.Phase,
			Scope:       step.Scope,
			Subject:     step.Subject,
			Kind:        step.Kind,
			Target:      step.Target,
			Disposition: step.Disposition,
		})
	}
	return out
}

func toGovernArbitrace(steps []flags.ArbitraceStep) []govern.ArbitraceStep {
	out := make([]govern.ArbitraceStep, 0, len(steps))
	return append(out, steps...)
}

func (s *Server) protoBundleSummary(bundle *Bundle) *arbiterv1.BundleSummary {
	if bundle == nil {
		return nil
	}
	active, _ := s.registry.GetActive(bundle.Name)
	return &arbiterv1.BundleSummary{
		BundleId:        bundle.ID,
		Name:            bundle.Name,
		Checksum:        bundle.Checksum,
		PublishedAt:     timestamppb.New(bundle.Published),
		RuleCount:       uint32(bundle.RuleCount),
		FlagCount:       uint32(bundle.FlagCount),
		ExpertRuleCount: uint32(bundle.ExpertRuleCount),
		StrategyCount:   uint32(bundle.StrategyCount),
		Active:          active != nil && active.ID == bundle.ID,
	}
}

func (s *Server) protoBundleOverrides(snapshot overrides.BundleSnapshot) *arbiterv1.BundleOverrides {
	if snapshot.BundleID == "" &&
		len(snapshot.Rules) == 0 &&
		len(snapshot.Flags) == 0 &&
		len(snapshot.FlagRules) == 0 &&
		len(snapshot.Strategies) == 0 {
		return nil
	}
	out := &arbiterv1.BundleOverrides{
		BundleId:   snapshot.BundleID,
		Rules:      make([]*arbiterv1.RuleOverrideEntry, 0, len(snapshot.Rules)),
		Flags:      make([]*arbiterv1.FlagOverrideEntry, 0, len(snapshot.Flags)),
		FlagRules:  make([]*arbiterv1.FlagRuleOverrideEntry, 0),
		Strategies: make([]*arbiterv1.StrategyOverrideEntry, 0),
	}
	for ruleName, ov := range snapshot.Rules {
		out.Rules = append(out.Rules, protoRuleOverrideEntry(ruleName, ov))
	}
	for flagKey, ov := range snapshot.Flags {
		out.Flags = append(out.Flags, protoFlagOverrideEntry(flagKey, ov))
	}
	for flagKey, rules := range snapshot.FlagRules {
		for idx, ov := range rules {
			out.FlagRules = append(out.FlagRules, protoFlagRuleOverrideEntry(flagKey, idx, ov))
		}
	}
	for strategyName, candidates := range snapshot.Strategies {
		for candidateLabel, ov := range candidates {
			out.Strategies = append(out.Strategies, protoStrategyOverrideEntry(strategyName, candidateLabel, ov))
		}
	}
	return out
}

func (s *Server) protoOverrideEvent(event overrides.OverrideEvent) *arbiterv1.OverrideEvent {
	snapshot := (*arbiterv1.BundleOverrides)(nil)
	if event.Type == overrides.OverrideEventSnapshot {
		snapshot = s.protoBundleOverrides(event.Snapshot)
	}
	out := &arbiterv1.OverrideEvent{
		Type:           protoOverrideEventType(event.Type),
		BundleId:       event.BundleID,
		Snapshot:       snapshot,
		RuleName:       event.RuleName,
		FlagKey:        event.FlagKey,
		StrategyName:   event.StrategyName,
		CandidateLabel: event.CandidateLabel,
	}
	switch event.Type {
	case overrides.OverrideEventRule:
		out.Rule = protoRuleOverrideEntry(event.RuleName, event.Rule)
	case overrides.OverrideEventFlag:
		out.Flag = protoFlagOverrideEntry(event.FlagKey, event.Flag)
	case overrides.OverrideEventFlagRule:
		out.RuleIndex = uint32(event.RuleIndex)
		out.FlagRule = protoFlagRuleOverrideEntry(event.FlagKey, event.RuleIndex, event.FlagRule)
	case overrides.OverrideEventStrategy:
		out.Strategy = protoStrategyOverrideEntry(event.StrategyName, event.CandidateLabel, event.Strategy)
	}
	return out
}

func protoRuleOverrideEntry(ruleName string, ov overrides.RuleOverride) *arbiterv1.RuleOverrideEntry {
	out := &arbiterv1.RuleOverrideEntry{
		RuleName:        ruleName,
		KillSwitchState: protoKillSwitchState(ov.KillSwitchState()),
	}
	if ov.KillSwitch != nil {
		out.KillSwitchSet = true
		out.KillSwitch = *ov.KillSwitch
	}
	if ov.Rollout != nil {
		out.RolloutSet = true
		out.Rollout = uint32(*ov.Rollout)
	}
	return out
}

func normalizeOverrideRollout(raw uint32) (uint16, error) {
	switch {
	case raw <= 100:
		return uint16(raw * 100), nil
	case raw <= uint32(govern.RolloutResolution):
		return uint16(raw), nil
	default:
		return 0, fmt.Errorf("rollout must be between 0 and 100 percent or 0 and %d basis points", govern.RolloutResolution)
	}
}

func protoFlagOverrideEntry(flagKey string, ov overrides.FlagOverride) *arbiterv1.FlagOverrideEntry {
	out := &arbiterv1.FlagOverrideEntry{
		FlagKey:         flagKey,
		KillSwitchState: protoKillSwitchState(ov.KillSwitchState()),
	}
	if ov.KillSwitch != nil {
		out.KillSwitchSet = true
		out.KillSwitch = *ov.KillSwitch
	}
	return out
}

func protoFlagRuleOverrideEntry(flagKey string, ruleIndex int, ov overrides.FlagRuleOverride) *arbiterv1.FlagRuleOverrideEntry {
	out := &arbiterv1.FlagRuleOverrideEntry{FlagKey: flagKey, RuleIndex: uint32(ruleIndex)}
	if ov.Rollout != nil {
		out.RolloutSet = true
		out.Rollout = uint32(*ov.Rollout)
	}
	return out
}

func protoStrategyOverrideEntry(strategyName, candidateLabel string, ov overrides.StrategyOverride) *arbiterv1.StrategyOverrideEntry {
	out := &arbiterv1.StrategyOverrideEntry{
		StrategyName:    strategyName,
		CandidateLabel:  candidateLabel,
		KillSwitchState: protoKillSwitchState(ov.KillSwitchState()),
	}
	if ov.KillSwitch != nil {
		out.KillSwitchSet = true
		out.KillSwitch = *ov.KillSwitch
	}
	if ov.Rollout != nil {
		out.RolloutSet = true
		out.Rollout = uint32(*ov.Rollout)
	}
	return out
}

func protoKillSwitchState(state ir.KillSwitchState) arbiterv1.KillSwitchState {
	switch state {
	case ir.KillSwitchOn:
		return arbiterv1.KillSwitchState_KILL_SWITCH_STATE_ON
	case ir.KillSwitchOff:
		return arbiterv1.KillSwitchState_KILL_SWITCH_STATE_OFF
	default:
		return arbiterv1.KillSwitchState_KILL_SWITCH_STATE_UNSPECIFIED
	}
}

func protoOverrideEventType(eventType overrides.OverrideEventType) arbiterv1.OverrideEventType {
	switch eventType {
	case overrides.OverrideEventSnapshot:
		return arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_SNAPSHOT
	case overrides.OverrideEventRule:
		return arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_RULE
	case overrides.OverrideEventFlag:
		return arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_FLAG
	case overrides.OverrideEventFlagRule:
		return arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_FLAG_RULE
	case overrides.OverrideEventStrategy:
		return arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_STRATEGY
	default:
		return arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_UNSPECIFIED
	}
}

const bundleEventSnapshot bundleEventType = "snapshot"

func uniqueNames(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Server) protoBundleEvent(event bundleEvent) *arbiterv1.BundleEvent {
	if event.Bundle == nil {
		return nil
	}
	return &arbiterv1.BundleEvent{
		Type:             s.protoBundleEventType(event.Type),
		Name:             event.Bundle.Name,
		Bundle:           s.protoBundleSummary(event.Bundle),
		Source:           append([]byte(nil), event.Bundle.Source...),
		PreviousBundleId: event.PreviousBundleID,
	}
}

func (s *Server) protoBundleEventType(eventType bundleEventType) arbiterv1.BundleEventType {
	switch eventType {
	case bundleEventSnapshot:
		return arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_SNAPSHOT
	case bundleEventPublished:
		return arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_PUBLISHED
	case bundleEventActivated:
		return arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_ACTIVATED
	case bundleEventRolledBack:
		return arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_ROLLED_BACK
	default:
		return arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_UNSPECIFIED
	}
}

var _ arbiterv1.ArbiterServiceServer = (*Server)(nil)
