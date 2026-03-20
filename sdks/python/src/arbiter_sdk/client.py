from __future__ import annotations

from collections.abc import Mapping, Sequence
from typing import Any

import grpc
from google.protobuf import struct_pb2, wrappers_pb2

from arbiter.v1 import service_pb2, service_pb2_grpc


def _to_struct(data: Mapping[str, Any] | None) -> struct_pb2.Struct:
    message = struct_pb2.Struct()
    if data:
        message.update(dict(data))
    return message


class ArbiterClient:
    def __init__(self, target: str, *, channel: grpc.Channel | None = None, options: Sequence[tuple[str, Any]] = ()) -> None:
        self._channel = channel or grpc.insecure_channel(target, options=tuple(options))
        self._owns_channel = channel is None
        self.stub = service_pb2_grpc.ArbiterServiceStub(self._channel)

    def close(self) -> None:
        if self._owns_channel:
            self._channel.close()

    def __enter__(self) -> "ArbiterClient":
        return self

    def __exit__(self, *_: object) -> None:
        self.close()

    def publish_bundle(self, name: str, source: str | bytes) -> service_pb2.PublishBundleResponse:
        payload = source.encode("utf-8") if isinstance(source, str) else source
        return self.stub.PublishBundle(service_pb2.PublishBundleRequest(name=name, source=payload))

    def list_bundles(self, *, name: str = "") -> service_pb2.ListBundlesResponse:
        return self.stub.ListBundles(service_pb2.ListBundlesRequest(name=name))

    def activate_bundle(self, name: str, bundle_id: str) -> service_pb2.ActivateBundleResponse:
        return self.stub.ActivateBundle(service_pb2.ActivateBundleRequest(name=name, bundle_id=bundle_id))

    def rollback_bundle(self, name: str) -> service_pb2.RollbackBundleResponse:
        return self.stub.RollbackBundle(service_pb2.RollbackBundleRequest(name=name))

    def get_bundle(self, *, bundle_id: str = "", bundle_name: str = "") -> service_pb2.GetBundleResponse:
        return self.stub.GetBundle(service_pb2.GetBundleRequest(bundle_id=bundle_id, bundle_name=bundle_name))

    def watch_bundles(self, *, names: Sequence[str] = (), active_only: bool = False):
        return self.stub.WatchBundles(service_pb2.WatchBundlesRequest(names=list(names), active_only=active_only))

    def get_overrides(self, *, bundle_id: str = "", bundle_name: str = "") -> service_pb2.GetOverridesResponse:
        return self.stub.GetOverrides(service_pb2.GetOverridesRequest(bundle_id=bundle_id, bundle_name=bundle_name))

    def watch_overrides(self, *, bundle_id: str):
        return self.stub.WatchOverrides(service_pb2.WatchOverridesRequest(bundle_id=bundle_id))

    def evaluate_rules(
        self,
        *,
        bundle_id: str = "",
        bundle_name: str = "",
        context: Mapping[str, Any] | None = None,
        request_id: str = "",
    ) -> service_pb2.EvaluateRulesResponse:
        return self.stub.EvaluateRules(
            service_pb2.EvaluateRulesRequest(
                bundle_id=bundle_id,
                bundle_name=bundle_name,
                context=_to_struct(context),
                request_id=request_id,
            )
        )

    def resolve_flag(
        self,
        flag_key: str,
        *,
        bundle_id: str = "",
        bundle_name: str = "",
        context: Mapping[str, Any] | None = None,
        request_id: str = "",
    ) -> service_pb2.ResolveFlagResponse:
        return self.stub.ResolveFlag(
            service_pb2.ResolveFlagRequest(
                bundle_id=bundle_id,
                bundle_name=bundle_name,
                flag_key=flag_key,
                context=_to_struct(context),
                request_id=request_id,
            )
        )

    def start_session(
        self,
        *,
        bundle_id: str = "",
        bundle_name: str = "",
        envelope: Mapping[str, Any] | None = None,
        facts: Sequence[Mapping[str, Any]] | None = None,
    ) -> service_pb2.StartSessionResponse:
        items = [
            service_pb2.ExpertFact(
                type=str(fact["type"]),
                key=str(fact["key"]),
                fields=_to_struct(fact.get("fields")),
            )
            for fact in (facts or [])
        ]
        return self.stub.StartSession(
            service_pb2.StartSessionRequest(
                bundle_id=bundle_id,
                bundle_name=bundle_name,
                envelope=_to_struct(envelope),
                facts=items,
            )
        )

    def run_session(self, session_id: str, *, request_id: str = "") -> service_pb2.RunSessionResponse:
        return self.stub.RunSession(service_pb2.RunSessionRequest(session_id=session_id, request_id=request_id))

    def assert_facts(self, session_id: str, facts: Sequence[Mapping[str, Any]]) -> service_pb2.AssertFactsResponse:
        items = [
            service_pb2.ExpertFact(
                type=str(fact["type"]),
                key=str(fact["key"]),
                fields=_to_struct(fact.get("fields")),
            )
            for fact in facts
        ]
        return self.stub.AssertFacts(service_pb2.AssertFactsRequest(session_id=session_id, facts=items))

    def retract_facts(self, session_id: str, facts: Sequence[Mapping[str, Any]]) -> service_pb2.RetractFactsResponse:
        items = [
            service_pb2.FactRef(type=str(fact["type"]), key=str(fact["key"]))
            for fact in facts
        ]
        return self.stub.RetractFacts(service_pb2.RetractFactsRequest(session_id=session_id, facts=items))

    def get_session_trace(self, session_id: str) -> service_pb2.GetSessionTraceResponse:
        return self.stub.GetSessionTrace(service_pb2.GetSessionTraceRequest(session_id=session_id))

    def close_session(self, session_id: str) -> service_pb2.CloseSessionResponse:
        return self.stub.CloseSession(service_pb2.CloseSessionRequest(session_id=session_id))

    def set_rule_override(
        self,
        bundle_id: str,
        rule_name: str,
        *,
        kill_switch: bool | None = None,
        rollout: int | None = None,
    ) -> service_pb2.SetRuleOverrideResponse:
        request = service_pb2.SetRuleOverrideRequest(bundle_id=bundle_id, rule_name=rule_name)
        if kill_switch is not None:
            request.kill_switch.CopyFrom(wrappers_pb2.BoolValue(value=kill_switch))
        if rollout is not None:
            request.rollout.CopyFrom(wrappers_pb2.UInt32Value(value=rollout))
        return self.stub.SetRuleOverride(request)

    def set_flag_override(
        self,
        bundle_id: str,
        flag_key: str,
        *,
        kill_switch: bool | None = None,
    ) -> service_pb2.SetFlagOverrideResponse:
        request = service_pb2.SetFlagOverrideRequest(bundle_id=bundle_id, flag_key=flag_key)
        if kill_switch is not None:
            request.kill_switch.CopyFrom(wrappers_pb2.BoolValue(value=kill_switch))
        return self.stub.SetFlagOverride(request)

    def set_flag_rule_override(
        self,
        bundle_id: str,
        flag_key: str,
        rule_index: int,
        *,
        rollout: int | None = None,
    ) -> service_pb2.SetFlagRuleOverrideResponse:
        request = service_pb2.SetFlagRuleOverrideRequest(
            bundle_id=bundle_id,
            flag_key=flag_key,
            rule_index=rule_index,
        )
        if rollout is not None:
            request.rollout.CopyFrom(wrappers_pb2.UInt32Value(value=rollout))
        return self.stub.SetFlagRuleOverride(request)
