package grpcserver

import (
	"context"
	"fmt"
	"time"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/audit"
	"github.com/odvcencio/arbiter/expert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// StartSession creates a new expert session for a published bundle.
func (s *Server) StartSession(_ context.Context, req *arbiterv1.StartSessionRequest) (*arbiterv1.StartSessionResponse, error) {
	bundle, err := s.bundleRef(req.GetBundleId(), req.GetBundleName())
	if err != nil {
		return nil, err
	}
	if bundle.ExpertRuleCount == 0 {
		return nil, status.Error(codes.FailedPrecondition, "bundle has no expert rules")
	}

	facts, err := expertFactsFromProto(req.GetFacts())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid facts: %v", err)
	}
	envelope := req.GetEnvelope().AsMap()
	session := expert.NewSession(bundle.Expert, envelope, facts, expert.Options{
		BundleID:  bundle.ID,
		Overrides: s.overrides,
	})
	handle := s.sessions.Create(bundle.ID, envelope, session)
	return &arbiterv1.StartSessionResponse{
		SessionId:       handle.ID,
		ExpertRuleCount: uint32(bundle.ExpertRuleCount),
	}, nil
}

// RunSession advances an expert session until quiescence or a guardrail.
func (s *Server) RunSession(ctx context.Context, req *arbiterv1.RunSessionRequest) (*arbiterv1.RunSessionResponse, error) {
	handle, err := s.lockSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	defer handle.mu.Unlock()

	mark := handle.Session.Checkpoint()
	if _, err := handle.Session.Run(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "run session: %v", err)
	}
	delta := handle.Session.DeltaSince(mark)
	resp, err := protoSessionResult(delta)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal session result: %v", err)
	}

	_ = s.audit.WriteDecision(ctx, audit.DecisionEvent{
		Timestamp: time.Now().UTC(),
		RequestID: req.GetRequestId(),
		BundleID:  handle.BundleID,
		Kind:      "expert",
		Context:   handle.Envelope,
		Expert:    auditExpertDecision(handle.ID, delta),
	})

	return resp, nil
}

// AssertFacts inserts or updates facts in an expert session.
func (s *Server) AssertFacts(_ context.Context, req *arbiterv1.AssertFactsRequest) (*arbiterv1.AssertFactsResponse, error) {
	handle, err := s.lockSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	defer handle.mu.Unlock()
	facts, err := expertFactsFromProto(req.GetFacts())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid facts: %v", err)
	}
	for _, fact := range facts {
		if err := handle.Session.Assert(fact); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "assert fact: %v", err)
		}
	}
	return &arbiterv1.AssertFactsResponse{}, nil
}

// RetractFacts removes facts from an expert session.
func (s *Server) RetractFacts(_ context.Context, req *arbiterv1.RetractFactsRequest) (*arbiterv1.RetractFactsResponse, error) {
	handle, err := s.lockSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	defer handle.mu.Unlock()
	for _, ref := range req.GetFacts() {
		if err := handle.Session.Retract(ref.GetType(), ref.GetKey()); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "retract fact: %v", err)
		}
	}
	return &arbiterv1.RetractFactsResponse{}, nil
}

// GetSessionTrace returns the current expert session state.
func (s *Server) GetSessionTrace(_ context.Context, req *arbiterv1.GetSessionTraceRequest) (*arbiterv1.GetSessionTraceResponse, error) {
	handle, err := s.lockSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	defer handle.mu.Unlock()
	resp, err := protoTraceSnapshot(handle.Session.Snapshot())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal session trace: %v", err)
	}
	return resp, nil
}

// CloseSession deterministically disposes of a live expert session.
func (s *Server) CloseSession(_ context.Context, req *arbiterv1.CloseSessionRequest) (*arbiterv1.CloseSessionResponse, error) {
	handle, err := s.lockSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	defer handle.mu.Unlock()
	s.sessions.Close(handle)
	return &arbiterv1.CloseSessionResponse{}, nil
}

func (s *Server) session(id string) (*ExpertSession, error) {
	handle, ok := s.sessions.Get(id)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %q not found", id)
	}
	return handle, nil
}

func (s *Server) lockSession(id string) (*ExpertSession, error) {
	handle, err := s.session(id)
	if err != nil {
		return nil, err
	}
	handle.mu.Lock()
	if handle.closed.Load() {
		handle.mu.Unlock()
		return nil, status.Errorf(codes.NotFound, "session %q not found", id)
	}
	return handle, nil
}

func expertFactsFromProto(items []*arbiterv1.ExpertFact) ([]expert.Fact, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]expert.Fact, 0, len(items))
	for _, item := range items {
		if item.GetType() == "" {
			return nil, fmt.Errorf("fact type is required")
		}
		if item.GetKey() == "" {
			return nil, fmt.Errorf("fact key is required")
		}
		fields := map[string]any{}
		if item.Fields != nil {
			fields = item.Fields.AsMap()
		}
		out = append(out, expert.Fact{
			Type:   item.GetType(),
			Key:    item.GetKey(),
			Fields: fields,
		})
	}
	return out, nil
}

func protoSessionResult(result expert.Result) (*arbiterv1.RunSessionResponse, error) {
	outcomes, err := protoOutcomes(result.Outcomes)
	if err != nil {
		return nil, err
	}
	facts, err := protoFacts(result.Facts)
	if err != nil {
		return nil, err
	}
	activations, err := protoActivations(result.Activations)
	if err != nil {
		return nil, err
	}
	return &arbiterv1.RunSessionResponse{
		Outcomes:    outcomes,
		Facts:       facts,
		Activations: activations,
		StopReason:  string(result.StopReason),
		Rounds:      uint32(result.Rounds),
		Mutations:   uint32(result.Mutations),
	}, nil
}

func protoTraceSnapshot(result expert.Result) (*arbiterv1.GetSessionTraceResponse, error) {
	outcomes, err := protoOutcomes(result.Outcomes)
	if err != nil {
		return nil, err
	}
	facts, err := protoFacts(result.Facts)
	if err != nil {
		return nil, err
	}
	activations, err := protoActivations(result.Activations)
	if err != nil {
		return nil, err
	}
	return &arbiterv1.GetSessionTraceResponse{
		Outcomes:    outcomes,
		Facts:       facts,
		Activations: activations,
		StopReason:  string(result.StopReason),
		Rounds:      uint32(result.Rounds),
		Mutations:   uint32(result.Mutations),
	}, nil
}

func protoOutcomes(items []expert.Outcome) ([]*arbiterv1.ExpertOutcome, error) {
	out := make([]*arbiterv1.ExpertOutcome, 0, len(items))
	for _, item := range items {
		params, err := structpb.NewStruct(cleanMap(item.Params))
		if err != nil {
			return nil, err
		}
		out = append(out, &arbiterv1.ExpertOutcome{
			Rule:   item.Rule,
			Name:   item.Name,
			Params: params,
		})
	}
	return out, nil
}

func protoFacts(items []expert.Fact) ([]*arbiterv1.ExpertFact, error) {
	out := make([]*arbiterv1.ExpertFact, 0, len(items))
	for _, item := range items {
		fields, err := structpb.NewStruct(cleanMap(item.Fields))
		if err != nil {
			return nil, err
		}
		out = append(out, &arbiterv1.ExpertFact{
			Type:   item.Type,
			Key:    item.Key,
			Fields: fields,
		})
	}
	return out, nil
}

func protoActivations(items []expert.Activation) ([]*arbiterv1.ExpertActivation, error) {
	out := make([]*arbiterv1.ExpertActivation, 0, len(items))
	for _, item := range items {
		params, err := structpb.NewStruct(cleanMap(item.Params))
		if err != nil {
			return nil, err
		}
		out = append(out, &arbiterv1.ExpertActivation{
			Round:   uint32(item.Round),
			Rule:    item.Rule,
			Kind:    string(item.Kind),
			Target:  item.Target,
			Params:  params,
			Changed: item.Changed,
			Detail:  item.Detail,
		})
	}
	return out, nil
}

func auditExpertDecision(sessionID string, result expert.Result) *audit.ExpertDecision {
	decision := &audit.ExpertDecision{
		SessionID:   sessionID,
		StopReason:  string(result.StopReason),
		Rounds:      result.Rounds,
		Mutations:   result.Mutations,
		Outcomes:    make([]audit.ExpertOutcome, 0, len(result.Outcomes)),
		Facts:       make([]audit.ExpertFact, 0, len(result.Facts)),
		Activations: make([]audit.ExpertActivation, 0, len(result.Activations)),
	}
	for _, outcome := range result.Outcomes {
		decision.Outcomes = append(decision.Outcomes, audit.ExpertOutcome{
			Rule:   outcome.Rule,
			Name:   outcome.Name,
			Params: outcome.Params,
		})
	}
	for _, fact := range result.Facts {
		decision.Facts = append(decision.Facts, audit.ExpertFact{
			Type:      fact.Type,
			Key:       fact.Key,
			Fields:    fact.Fields,
			DerivedBy: append([]string(nil), fact.DerivedBy...),
		})
	}
	for _, activation := range result.Activations {
		decision.Activations = append(decision.Activations, audit.ExpertActivation{
			Round:   activation.Round,
			Rule:    activation.Rule,
			Kind:    string(activation.Kind),
			Target:  activation.Target,
			Params:  activation.Params,
			Changed: activation.Changed,
			Detail:  activation.Detail,
		})
	}
	return decision
}
