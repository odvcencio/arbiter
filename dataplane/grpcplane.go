package dataplane

import (
	"context"
	"errors"
	"time"

	arbiterv1 "github.com/odvcencio/arbiter/api/arbiter/v1"
	"github.com/odvcencio/arbiter/overrides"
)

// GRPCControlPlane adapts the Arbiter gRPC control plane to the local agent contract.
type GRPCControlPlane struct {
	client arbiterv1.ArbiterServiceClient
}

// NewGRPCControlPlane creates a bundle sync control plane backed by gRPC.
func NewGRPCControlPlane(client arbiterv1.ArbiterServiceClient) *GRPCControlPlane {
	return &GRPCControlPlane{client: client}
}

// GetBundle fetches one bundle source payload from the control plane.
func (g *GRPCControlPlane) GetBundle(ctx context.Context, locator BundleLocator) (*Bundle, error) {
	if g == nil || g.client == nil {
		return nil, errors.New("control plane client is required")
	}
	resp, err := g.client.GetBundle(ctx, &arbiterv1.GetBundleRequest{
		BundleId:   locator.BundleID,
		BundleName: locator.Name,
	})
	if err != nil {
		return nil, err
	}
	return bundleFromProto(resp.GetBundle(), resp.GetSource()), nil
}

// WatchBundles opens a long-lived update stream for one bundle name.
func (g *GRPCControlPlane) WatchBundles(ctx context.Context, req WatchRequest) (BundleStream, error) {
	if g == nil || g.client == nil {
		return nil, errors.New("control plane client is required")
	}
	names := make([]string, 0, 1)
	if req.Name != "" {
		names = append(names, req.Name)
	}
	stream, err := g.client.WatchBundles(ctx, &arbiterv1.WatchBundlesRequest{
		Names:      names,
		ActiveOnly: req.ActiveOnly,
	})
	if err != nil {
		return nil, err
	}
	return &grpcBundleStream{stream: stream}, nil
}

// GetOverrides fetches the current override snapshot for one bundle.
func (g *GRPCControlPlane) GetOverrides(ctx context.Context, locator OverrideLocator) (*overrides.Snapshot, error) {
	if g == nil || g.client == nil {
		return nil, errors.New("control plane client is required")
	}
	resp, err := g.client.GetOverrides(ctx, &arbiterv1.GetOverridesRequest{
		BundleId:   locator.BundleID,
		BundleName: locator.Name,
	})
	if err != nil {
		return nil, err
	}
	return snapshotFromProto(resp.GetOverrides()), nil
}

// WatchOverrides opens a long-lived update stream for one immutable bundle id.
func (g *GRPCControlPlane) WatchOverrides(ctx context.Context, locator OverrideLocator) (OverrideStream, error) {
	if g == nil || g.client == nil {
		return nil, errors.New("control plane client is required")
	}
	if locator.BundleID == "" {
		return nil, errors.New("override watch requires bundle id")
	}
	stream, err := g.client.WatchOverrides(ctx, &arbiterv1.WatchOverridesRequest{
		BundleId: locator.BundleID,
	})
	if err != nil {
		return nil, err
	}
	return &grpcOverrideStream{
		stream:   stream,
		bundleID: locator.BundleID,
	}, nil
}

type grpcBundleStream struct {
	stream arbiterv1.ArbiterService_WatchBundlesClient
}

type grpcOverrideStream struct {
	stream   arbiterv1.ArbiterService_WatchOverridesClient
	bundleID string
	current  overrides.Snapshot
}

func (s *grpcBundleStream) Recv() (*BundleEvent, error) {
	event, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}
	if event == nil {
		return nil, nil
	}
	return &BundleEvent{
		Type:   bundleEventTypeFromProto(event.GetType()),
		Bundle: *bundleFromProto(event.GetBundle(), event.GetSource()),
	}, nil
}

func (s *grpcBundleStream) Close() error {
	return s.stream.CloseSend()
}

func (s *grpcOverrideStream) Recv() (*OverrideEvent, error) {
	event, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}
	if event == nil {
		return nil, nil
	}

	if event.GetType() == arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_SNAPSHOT {
		s.current = *snapshotFromProto(event.GetSnapshot())
	} else {
		applyProtoOverrideMutation(&s.current, firstNonEmpty(event.GetBundleId(), s.bundleID), event)
	}

	return &OverrideEvent{
		Type:     overrideEventTypeFromProto(event.GetType()),
		BundleID: firstNonEmpty(event.GetBundleId(), s.bundleID),
		Snapshot: cloneOverrideSnapshot(s.current),
	}, nil
}

func (s *grpcOverrideStream) Close() error {
	return s.stream.CloseSend()
}

func bundleFromProto(summary *arbiterv1.BundleSummary, source []byte) *Bundle {
	if summary == nil {
		return &Bundle{Source: append([]byte(nil), source...)}
	}
	return &Bundle{
		ID:          summary.GetBundleId(),
		Name:        summary.GetName(),
		Checksum:    summary.GetChecksum(),
		Source:      append([]byte(nil), source...),
		PublishedAt: protoTime(summary.GetPublishedAt()),
		Active:      summary.GetActive(),
	}
}

func bundleEventTypeFromProto(eventType arbiterv1.BundleEventType) BundleEventType {
	switch eventType {
	case arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_SNAPSHOT:
		return BundleEventSnapshot
	case arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_PUBLISHED:
		return BundleEventPublished
	case arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_ACTIVATED:
		return BundleEventActivated
	case arbiterv1.BundleEventType_BUNDLE_EVENT_TYPE_ROLLED_BACK:
		return BundleEventRolled
	default:
		return BundleEventUnknown
	}
}

func overrideEventTypeFromProto(eventType arbiterv1.OverrideEventType) OverrideEventType {
	switch eventType {
	case arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_SNAPSHOT:
		return OverrideEventSnapshot
	case arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_RULE, arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_FLAG, arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_FLAG_RULE:
		return OverrideEventMutation
	default:
		return OverrideEventUnknown
	}
}

func snapshotFromProto(items *arbiterv1.BundleOverrides) *overrides.Snapshot {
	snapshot := &overrides.Snapshot{}
	if items == nil || items.GetBundleId() == "" {
		return snapshot
	}
	bundleID := items.GetBundleId()
	if len(items.GetRules()) > 0 {
		snapshot.Rules = map[string]map[string]overrides.RuleOverride{
			bundleID: make(map[string]overrides.RuleOverride, len(items.GetRules())),
		}
		for _, entry := range items.GetRules() {
			snapshot.Rules[bundleID][entry.GetRuleName()] = ruleOverrideFromProto(entry)
		}
	}
	if len(items.GetFlags()) > 0 {
		snapshot.Flags = map[string]map[string]overrides.FlagOverride{
			bundleID: make(map[string]overrides.FlagOverride, len(items.GetFlags())),
		}
		for _, entry := range items.GetFlags() {
			snapshot.Flags[bundleID][entry.GetFlagKey()] = flagOverrideFromProto(entry)
		}
	}
	if len(items.GetFlagRules()) > 0 {
		snapshot.FlagRules = map[string]map[string]map[int]overrides.FlagRuleOverride{
			bundleID: make(map[string]map[int]overrides.FlagRuleOverride),
		}
		for _, entry := range items.GetFlagRules() {
			rules := snapshot.FlagRules[bundleID][entry.GetFlagKey()]
			if rules == nil {
				rules = make(map[int]overrides.FlagRuleOverride)
				snapshot.FlagRules[bundleID][entry.GetFlagKey()] = rules
			}
			rules[int(entry.GetRuleIndex())] = flagRuleOverrideFromProto(entry)
		}
	}
	return snapshot
}

func applyProtoOverrideMutation(snapshot *overrides.Snapshot, bundleID string, event *arbiterv1.OverrideEvent) {
	if snapshot == nil || bundleID == "" || event == nil {
		return
	}
	switch event.GetType() {
	case arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_RULE:
		applyRuleMutation(snapshot, bundleID, event.GetRule())
	case arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_FLAG:
		applyFlagMutation(snapshot, bundleID, event.GetFlag())
	case arbiterv1.OverrideEventType_OVERRIDE_EVENT_TYPE_FLAG_RULE:
		applyFlagRuleMutation(snapshot, bundleID, event.GetFlagRule())
	}
}

func applyRuleMutation(snapshot *overrides.Snapshot, bundleID string, entry *arbiterv1.RuleOverrideEntry) {
	if entry == nil {
		return
	}
	if !entry.GetKillSwitchSet() && !entry.GetRolloutSet() {
		if rules := snapshot.Rules[bundleID]; rules != nil {
			delete(rules, entry.GetRuleName())
			if len(rules) == 0 {
				delete(snapshot.Rules, bundleID)
			}
		}
		return
	}
	if snapshot.Rules == nil {
		snapshot.Rules = make(map[string]map[string]overrides.RuleOverride)
	}
	rules := snapshot.Rules[bundleID]
	if rules == nil {
		rules = make(map[string]overrides.RuleOverride)
		snapshot.Rules[bundleID] = rules
	}
	rules[entry.GetRuleName()] = ruleOverrideFromProto(entry)
}

func applyFlagMutation(snapshot *overrides.Snapshot, bundleID string, entry *arbiterv1.FlagOverrideEntry) {
	if entry == nil {
		return
	}
	if !entry.GetKillSwitchSet() {
		if flags := snapshot.Flags[bundleID]; flags != nil {
			delete(flags, entry.GetFlagKey())
			if len(flags) == 0 {
				delete(snapshot.Flags, bundleID)
			}
		}
		return
	}
	if snapshot.Flags == nil {
		snapshot.Flags = make(map[string]map[string]overrides.FlagOverride)
	}
	flags := snapshot.Flags[bundleID]
	if flags == nil {
		flags = make(map[string]overrides.FlagOverride)
		snapshot.Flags[bundleID] = flags
	}
	flags[entry.GetFlagKey()] = flagOverrideFromProto(entry)
}

func applyFlagRuleMutation(snapshot *overrides.Snapshot, bundleID string, entry *arbiterv1.FlagRuleOverrideEntry) {
	if entry == nil {
		return
	}
	if !entry.GetRolloutSet() {
		if flags := snapshot.FlagRules[bundleID]; flags != nil {
			if rules := flags[entry.GetFlagKey()]; rules != nil {
				delete(rules, int(entry.GetRuleIndex()))
				if len(rules) == 0 {
					delete(flags, entry.GetFlagKey())
				}
			}
			if len(flags) == 0 {
				delete(snapshot.FlagRules, bundleID)
			}
		}
		return
	}
	if snapshot.FlagRules == nil {
		snapshot.FlagRules = make(map[string]map[string]map[int]overrides.FlagRuleOverride)
	}
	flags := snapshot.FlagRules[bundleID]
	if flags == nil {
		flags = make(map[string]map[int]overrides.FlagRuleOverride)
		snapshot.FlagRules[bundleID] = flags
	}
	rules := flags[entry.GetFlagKey()]
	if rules == nil {
		rules = make(map[int]overrides.FlagRuleOverride)
		flags[entry.GetFlagKey()] = rules
	}
	rules[int(entry.GetRuleIndex())] = flagRuleOverrideFromProto(entry)
}

func ruleOverrideFromProto(entry *arbiterv1.RuleOverrideEntry) overrides.RuleOverride {
	ov := overrides.RuleOverride{}
	if entry != nil && entry.GetKillSwitchSet() {
		v := entry.GetKillSwitch()
		ov.KillSwitch = &v
	}
	if entry != nil && entry.GetRolloutSet() {
		v := uint16(entry.GetRollout())
		ov.Rollout = &v
	}
	return ov
}

func flagOverrideFromProto(entry *arbiterv1.FlagOverrideEntry) overrides.FlagOverride {
	ov := overrides.FlagOverride{}
	if entry != nil && entry.GetKillSwitchSet() {
		v := entry.GetKillSwitch()
		ov.KillSwitch = &v
	}
	return ov
}

func flagRuleOverrideFromProto(entry *arbiterv1.FlagRuleOverrideEntry) overrides.FlagRuleOverride {
	ov := overrides.FlagRuleOverride{}
	if entry != nil && entry.GetRolloutSet() {
		v := uint16(entry.GetRollout())
		ov.Rollout = &v
	}
	return ov
}

func cloneOverrideSnapshot(snapshot overrides.Snapshot) overrides.Snapshot {
	out := overrides.Snapshot{}
	if len(snapshot.Rules) > 0 {
		out.Rules = make(map[string]map[string]overrides.RuleOverride, len(snapshot.Rules))
		for bundleID, rules := range snapshot.Rules {
			cloned := make(map[string]overrides.RuleOverride, len(rules))
			for name, ov := range rules {
				cloned[name] = cloneRuleOverride(ov)
			}
			out.Rules[bundleID] = cloned
		}
	}
	if len(snapshot.Flags) > 0 {
		out.Flags = make(map[string]map[string]overrides.FlagOverride, len(snapshot.Flags))
		for bundleID, flags := range snapshot.Flags {
			cloned := make(map[string]overrides.FlagOverride, len(flags))
			for key, ov := range flags {
				cloned[key] = cloneFlagOverride(ov)
			}
			out.Flags[bundleID] = cloned
		}
	}
	if len(snapshot.FlagRules) > 0 {
		out.FlagRules = make(map[string]map[string]map[int]overrides.FlagRuleOverride, len(snapshot.FlagRules))
		for bundleID, flags := range snapshot.FlagRules {
			clonedFlags := make(map[string]map[int]overrides.FlagRuleOverride, len(flags))
			for key, rules := range flags {
				clonedRules := make(map[int]overrides.FlagRuleOverride, len(rules))
				for idx, ov := range rules {
					clonedRules[idx] = cloneFlagRuleOverride(ov)
				}
				clonedFlags[key] = clonedRules
			}
			out.FlagRules[bundleID] = clonedFlags
		}
	}
	return out
}

func cloneRuleOverride(ov overrides.RuleOverride) overrides.RuleOverride {
	out := overrides.RuleOverride{}
	if ov.KillSwitch != nil {
		v := *ov.KillSwitch
		out.KillSwitch = &v
	}
	if ov.Rollout != nil {
		v := *ov.Rollout
		out.Rollout = &v
	}
	return out
}

func cloneFlagOverride(ov overrides.FlagOverride) overrides.FlagOverride {
	out := overrides.FlagOverride{}
	if ov.KillSwitch != nil {
		v := *ov.KillSwitch
		out.KillSwitch = &v
	}
	return out
}

func cloneFlagRuleOverride(ov overrides.FlagRuleOverride) overrides.FlagRuleOverride {
	out := overrides.FlagRuleOverride{}
	if ov.Rollout != nil {
		v := *ov.Rollout
		out.Rollout = &v
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func protoTime(ts interface{ AsTime() time.Time }) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

var _ ControlPlane = (*GRPCControlPlane)(nil)
var _ BundleStream = (*grpcBundleStream)(nil)
var _ OverrideControlPlane = (*GRPCControlPlane)(nil)
var _ OverrideStream = (*grpcOverrideStream)(nil)
