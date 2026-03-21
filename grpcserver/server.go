package grpcserver

import (
	"context"
	"fmt"
	"time"

	arbiter "github.com/odvcencio/arbiter"
	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/flags"
	"github.com/odvcencio/arbiter/govern"
	"github.com/odvcencio/arbiter/overrides"
	"github.com/odvcencio/arbiter/vm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements the Arbiter gRPC API.
type Server struct {
	arbiterv1.UnimplementedArbiterServiceServer
	registry  *Registry
	sessions  *SessionStore
	overrides *overrides.Store
	audit     audit.Sink
}

// NewServer creates a gRPC service server.
func NewServer(registry *Registry, store *overrides.Store, sink audit.Sink) *Server {
	if registry == nil {
		registry = NewRegistry()
	}
	if store == nil {
		store = overrides.NewStore()
	}
	if sink == nil {
		sink = audit.NopSink{}
	}
	return &Server{
		registry:  registry,
		sessions:  NewSessionStore(),
		overrides: store,
		audit:     sink,
	}
}

// PublishBundle compiles and stores a governed bundle.
func (s *Server) PublishBundle(ctx context.Context, req *arbiterv1.PublishBundleRequest) (*arbiterv1.PublishBundleResponse, error) {
	if len(req.GetSource()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "source is required")
	}
	bundle, err := s.registry.Publish(req.GetName(), req.GetSource())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "publish bundle: %v", err)
	}
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
		return nil, status.Errorf(codes.InvalidArgument, "activate bundle: %v", err)
	}
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
		return nil, status.Errorf(codes.InvalidArgument, "rollback bundle: %v", err)
	}
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

// EvaluateRules evaluates rules in a published bundle.
func (s *Server) EvaluateRules(ctx context.Context, req *arbiterv1.EvaluateRulesRequest) (*arbiterv1.EvaluateRulesResponse, error) {
	bundle, err := s.bundleRef(req.GetBundleId(), req.GetBundleName())
	if err != nil {
		return nil, err
	}
	if bundle.RuleCount == 0 {
		return nil, status.Error(codes.FailedPrecondition, "bundle has no rules")
	}

	ctxMap := req.GetContext().AsMap()
	dc := arbiter.DataFromMap(ctxMap, bundle.Compiled.Ruleset)
	matched, trace, err := arbiter.EvalGovernedWithOverrides(bundle.Compiled.Ruleset, dc, bundle.Compiled.Segments, ctxMap, bundle.ID, s.overrides)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "evaluate rules: %v", err)
	}

	resp := &arbiterv1.EvaluateRulesResponse{
		Matched: make([]*arbiterv1.RuleMatch, 0, len(matched)),
		Trace:   protoTrace(trace.Steps),
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
		Timestamp: time.Now().UTC(),
		RequestID: req.GetRequestId(),
		BundleID:  bundle.ID,
		Kind:      "rules",
		Context:   ctxMap,
		Rules:     auditRuleMatches(matched),
		Trace:     trace.Steps,
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

	ctxMap := req.GetContext().AsMap()
	eval := bundle.Flags.ExplainWithOverrides(bundle.ID, req.GetFlagKey(), ctxMap, s.overrides)
	values, err := structpb.NewStruct(cleanMap(eval.Variant.Values))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal values: %v", err)
	}

	env := bundle.Flags.Environment
	event := audit.DecisionEvent{
		Timestamp:   time.Now().UTC(),
		RequestID:   req.GetRequestId(),
		BundleID:    bundle.ID,
		Environment: env,
		Kind:        "flag",
		Context:     ctxMap,
		Flag: &audit.FlagDecision{
			Flag:        eval.Flag,
			Variant:     eval.Variant.Name,
			Values:      eval.Variant.Values,
			IsDefault:   eval.IsDefault,
			Reason:      eval.Reason,
			Environment: env,
		},
		Trace: toGovernTrace(eval.Trace),
	}

	// Emit assignment event for non-default resolutions (experimentation).
	if !eval.IsDefault {
		userID, _ := extractUserID(ctxMap)
		event.Assignment = &audit.FlagAssignment{
			Flag:        eval.Flag,
			Variant:     eval.Variant.Name,
			UserID:      userID,
			Environment: env,
			Values:      eval.Variant.Values,
		}
	}

	_ = s.audit.WriteDecision(ctx, event)

	return &arbiterv1.ResolveFlagResponse{
		Variant:   eval.Variant.Name,
		Values:    values,
		IsDefault: eval.IsDefault,
		Reason:    eval.Reason,
		Trace:     protoTrace(eval.Trace),
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
			Scope:      "rule",
			Target:     req.GetRuleName(),
			KillSwitch: ov.KillSwitch,
			Rollout:    ov.Rollout,
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
			Scope:      "flag",
			Target:     req.GetFlagKey(),
			KillSwitch: ov.KillSwitch,
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

func protoTrace(steps []govern.TraceStep) []*arbiterv1.TraceStep {
	out := make([]*arbiterv1.TraceStep, 0, len(steps))
	for _, step := range steps {
		out = append(out, &arbiterv1.TraceStep{
			Check:  step.Check,
			Result: step.Result,
			Detail: step.Detail,
		})
	}
	return out
}

func toGovernTrace(steps []flags.TraceStep) []govern.TraceStep {
	out := make([]govern.TraceStep, 0, len(steps))
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
		Active:          active != nil && active.ID == bundle.ID,
	}
}

func (s *Server) protoBundleOverrides(snapshot overrides.BundleSnapshot) *arbiterv1.BundleOverrides {
	if snapshot.BundleID == "" && len(snapshot.Rules) == 0 && len(snapshot.Flags) == 0 && len(snapshot.FlagRules) == 0 {
		return nil
	}
	out := &arbiterv1.BundleOverrides{
		BundleId:  snapshot.BundleID,
		Rules:     make([]*arbiterv1.RuleOverrideEntry, 0, len(snapshot.Rules)),
		Flags:     make([]*arbiterv1.FlagOverrideEntry, 0, len(snapshot.Flags)),
		FlagRules: make([]*arbiterv1.FlagRuleOverrideEntry, 0),
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
	return out
}

func (s *Server) protoOverrideEvent(event overrides.OverrideEvent) *arbiterv1.OverrideEvent {
	snapshot := (*arbiterv1.BundleOverrides)(nil)
	if event.Type == overrides.OverrideEventSnapshot {
		snapshot = s.protoBundleOverrides(event.Snapshot)
	}
	out := &arbiterv1.OverrideEvent{
		Type:     protoOverrideEventType(event.Type),
		BundleId: event.BundleID,
		Snapshot: snapshot,
		RuleName: event.RuleName,
		FlagKey:  event.FlagKey,
	}
	switch event.Type {
	case overrides.OverrideEventRule:
		out.Rule = protoRuleOverrideEntry(event.RuleName, event.Rule)
	case overrides.OverrideEventFlag:
		out.Flag = protoFlagOverrideEntry(event.FlagKey, event.Flag)
	case overrides.OverrideEventFlagRule:
		out.RuleIndex = uint32(event.RuleIndex)
		out.FlagRule = protoFlagRuleOverrideEntry(event.FlagKey, event.RuleIndex, event.FlagRule)
	}
	return out
}

func protoRuleOverrideEntry(ruleName string, ov overrides.RuleOverride) *arbiterv1.RuleOverrideEntry {
	out := &arbiterv1.RuleOverrideEntry{RuleName: ruleName}
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
	out := &arbiterv1.FlagOverrideEntry{FlagKey: flagKey}
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
