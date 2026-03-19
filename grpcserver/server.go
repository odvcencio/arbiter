package grpcserver

import (
	"context"
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
func (s *Server) PublishBundle(_ context.Context, req *arbiterv1.PublishBundleRequest) (*arbiterv1.PublishBundleResponse, error) {
	if len(req.GetSource()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "source is required")
	}
	bundle, err := s.registry.Publish(req.GetName(), req.GetSource())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "publish bundle: %v", err)
	}
	return &arbiterv1.PublishBundleResponse{
		BundleId:        bundle.ID,
		Checksum:        bundle.Checksum,
		RuleCount:       uint32(bundle.RuleCount),
		FlagCount:       uint32(bundle.FlagCount),
		ExpertRuleCount: uint32(bundle.ExpertRuleCount),
		PublishedAt:     timestamppb.New(bundle.Published),
	}, nil
}

// EvaluateRules evaluates rules in a published bundle.
func (s *Server) EvaluateRules(ctx context.Context, req *arbiterv1.EvaluateRulesRequest) (*arbiterv1.EvaluateRulesResponse, error) {
	bundle, err := s.bundle(req.GetBundleId())
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
	bundle, err := s.bundle(req.GetBundleId())
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

	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		RequestID: req.GetRequestId(),
		BundleID:  bundle.ID,
		Kind:      "flag",
		Context:   ctxMap,
		Flag: &audit.FlagDecision{
			Flag:      eval.Flag,
			Variant:   eval.Variant.Name,
			Values:    eval.Variant.Values,
			IsDefault: eval.IsDefault,
			Reason:    eval.Reason,
		},
		Trace: toGovernTrace(eval.Trace),
	})

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
		if req.Rollout.GetValue() > 100 {
			return nil, status.Error(codes.InvalidArgument, "rollout must be between 0 and 100")
		}
		v := uint8(req.Rollout.GetValue())
		ov.Rollout = &v
	}
	s.overrides.SetRule(req.GetBundleId(), req.GetRuleName(), ov)
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
	s.overrides.SetFlag(req.GetBundleId(), req.GetFlagKey(), ov)
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
		if req.Rollout.GetValue() > 100 {
			return nil, status.Error(codes.InvalidArgument, "rollout must be between 0 and 100")
		}
		v := uint8(req.Rollout.GetValue())
		ov.Rollout = &v
	}
	s.overrides.SetFlagRule(req.GetBundleId(), req.GetFlagKey(), int(req.GetRuleIndex()), ov)
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
	for _, step := range steps {
		out = append(out, govern.TraceStep{
			Check:  step.Check,
			Result: step.Result,
			Detail: step.Detail,
		})
	}
	return out
}

var _ arbiterv1.ArbiterServiceServer = (*Server)(nil)
