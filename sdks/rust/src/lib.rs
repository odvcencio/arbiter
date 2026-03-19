use prost_types::{value::Kind, Struct, Value};
use serde_json::Value as JsonValue;
use tonic::transport::{Channel, Endpoint};

pub mod arbiter {
    pub mod v1 {
        tonic::include_proto!("arbiter.v1");
    }
}

use arbiter::v1::{
    arbiter_service_client::ArbiterServiceClient, ActivateBundleRequest, ActivateBundleResponse,
    AssertFactsRequest, AssertFactsResponse, CloseSessionRequest, CloseSessionResponse,
    EvaluateRulesRequest, EvaluateRulesResponse, ExpertFact, FactRef, GetSessionTraceRequest,
    GetSessionTraceResponse, ListBundlesRequest, ListBundlesResponse, PublishBundleRequest,
    PublishBundleResponse, ResolveFlagRequest, ResolveFlagResponse, RetractFactsRequest,
    RetractFactsResponse, RollbackBundleRequest, RollbackBundleResponse, RunSessionRequest,
    RunSessionResponse, SetFlagOverrideRequest, SetFlagOverrideResponse,
    SetFlagRuleOverrideRequest, SetFlagRuleOverrideResponse, SetRuleOverrideRequest,
    SetRuleOverrideResponse, StartSessionRequest, StartSessionResponse,
};

#[derive(Clone)]
pub struct ArbiterClient {
    inner: ArbiterServiceClient<Channel>,
}

impl ArbiterClient {
    pub async fn connect(dst: impl Into<String>) -> Result<Self, tonic::transport::Error> {
        let endpoint = Endpoint::from_shared(dst.into())?;
        let inner = ArbiterServiceClient::connect(endpoint).await?;
        Ok(Self { inner })
    }

    pub async fn publish_bundle(
        &mut self,
        name: impl Into<String>,
        source: impl Into<Vec<u8>>,
    ) -> Result<PublishBundleResponse, tonic::Status> {
        Ok(self
            .inner
            .publish_bundle(PublishBundleRequest {
                name: name.into(),
                source: source.into(),
            })
            .await?
            .into_inner())
    }

    pub async fn list_bundles(&mut self, name: impl Into<String>) -> Result<ListBundlesResponse, tonic::Status> {
        Ok(self
            .inner
            .list_bundles(ListBundlesRequest { name: name.into() })
            .await?
            .into_inner())
    }

    pub async fn activate_bundle(
        &mut self,
        name: impl Into<String>,
        bundle_id: impl Into<String>,
    ) -> Result<ActivateBundleResponse, tonic::Status> {
        Ok(self
            .inner
            .activate_bundle(ActivateBundleRequest {
                name: name.into(),
                bundle_id: bundle_id.into(),
            })
            .await?
            .into_inner())
    }

    pub async fn rollback_bundle(
        &mut self,
        name: impl Into<String>,
    ) -> Result<RollbackBundleResponse, tonic::Status> {
        Ok(self
            .inner
            .rollback_bundle(RollbackBundleRequest { name: name.into() })
            .await?
            .into_inner())
    }

    pub async fn evaluate_rules_by_name(
        &mut self,
        bundle_name: impl Into<String>,
        context: JsonValue,
        request_id: impl Into<String>,
    ) -> Result<EvaluateRulesResponse, tonic::Status> {
        Ok(self
            .inner
            .evaluate_rules(EvaluateRulesRequest {
                bundle_id: String::new(),
                bundle_name: bundle_name.into(),
                context: Some(json_to_struct(context)),
                request_id: request_id.into(),
            })
            .await?
            .into_inner())
    }

    pub async fn resolve_flag_by_name(
        &mut self,
        bundle_name: impl Into<String>,
        flag_key: impl Into<String>,
        context: JsonValue,
        request_id: impl Into<String>,
    ) -> Result<ResolveFlagResponse, tonic::Status> {
        Ok(self
            .inner
            .resolve_flag(ResolveFlagRequest {
                bundle_id: String::new(),
                bundle_name: bundle_name.into(),
                flag_key: flag_key.into(),
                context: Some(json_to_struct(context)),
                request_id: request_id.into(),
            })
            .await?
            .into_inner())
    }

    pub async fn start_session_by_name(
        &mut self,
        bundle_name: impl Into<String>,
        envelope: JsonValue,
        facts: Vec<ExpertFact>,
    ) -> Result<StartSessionResponse, tonic::Status> {
        Ok(self
            .inner
            .start_session(StartSessionRequest {
                bundle_id: String::new(),
                bundle_name: bundle_name.into(),
                envelope: Some(json_to_struct(envelope)),
                facts,
            })
            .await?
            .into_inner())
    }

    pub async fn run_session(
        &mut self,
        session_id: impl Into<String>,
        request_id: impl Into<String>,
    ) -> Result<RunSessionResponse, tonic::Status> {
        Ok(self
            .inner
            .run_session(RunSessionRequest {
                session_id: session_id.into(),
                request_id: request_id.into(),
            })
            .await?
            .into_inner())
    }

    pub async fn assert_facts(
        &mut self,
        session_id: impl Into<String>,
        facts: Vec<ExpertFact>,
    ) -> Result<AssertFactsResponse, tonic::Status> {
        Ok(self
            .inner
            .assert_facts(AssertFactsRequest {
                session_id: session_id.into(),
                facts,
            })
            .await?
            .into_inner())
    }

    pub async fn retract_facts(
        &mut self,
        session_id: impl Into<String>,
        facts: Vec<FactRef>,
    ) -> Result<RetractFactsResponse, tonic::Status> {
        Ok(self
            .inner
            .retract_facts(RetractFactsRequest {
                session_id: session_id.into(),
                facts,
            })
            .await?
            .into_inner())
    }

    pub async fn get_session_trace(
        &mut self,
        session_id: impl Into<String>,
    ) -> Result<GetSessionTraceResponse, tonic::Status> {
        Ok(self
            .inner
            .get_session_trace(GetSessionTraceRequest {
                session_id: session_id.into(),
            })
            .await?
            .into_inner())
    }

    pub async fn close_session(
        &mut self,
        session_id: impl Into<String>,
    ) -> Result<CloseSessionResponse, tonic::Status> {
        Ok(self
            .inner
            .close_session(CloseSessionRequest {
                session_id: session_id.into(),
            })
            .await?
            .into_inner())
    }

    pub async fn set_rule_override(
        &mut self,
        bundle_id: impl Into<String>,
        rule_name: impl Into<String>,
        kill_switch: Option<bool>,
        rollout: Option<u32>,
    ) -> Result<SetRuleOverrideResponse, tonic::Status> {
        Ok(self
            .inner
            .set_rule_override(SetRuleOverrideRequest {
                bundle_id: bundle_id.into(),
                rule_name: rule_name.into(),
                kill_switch,
                rollout,
            })
            .await?
            .into_inner())
    }

    pub async fn set_flag_override(
        &mut self,
        bundle_id: impl Into<String>,
        flag_key: impl Into<String>,
        kill_switch: Option<bool>,
    ) -> Result<SetFlagOverrideResponse, tonic::Status> {
        Ok(self
            .inner
            .set_flag_override(SetFlagOverrideRequest {
                bundle_id: bundle_id.into(),
                flag_key: flag_key.into(),
                kill_switch,
            })
            .await?
            .into_inner())
    }

    pub async fn set_flag_rule_override(
        &mut self,
        bundle_id: impl Into<String>,
        flag_key: impl Into<String>,
        rule_index: u32,
        rollout: Option<u32>,
    ) -> Result<SetFlagRuleOverrideResponse, tonic::Status> {
        Ok(self
            .inner
            .set_flag_rule_override(SetFlagRuleOverrideRequest {
                bundle_id: bundle_id.into(),
                flag_key: flag_key.into(),
                rule_index,
                rollout,
            })
            .await?
            .into_inner())
    }
}

pub fn json_to_struct(value: JsonValue) -> Struct {
    match value {
        JsonValue::Object(map) => Struct {
            fields: map.into_iter().map(|(k, v)| (k, json_to_proto(v))).collect(),
        },
        JsonValue::Null => Struct { fields: Default::default() },
        other => panic!("expected JSON object for protobuf Struct, got {other}"),
    }
}

pub fn fact(typ: impl Into<String>, key: impl Into<String>, fields: JsonValue) -> ExpertFact {
    ExpertFact {
        r#type: typ.into(),
        key: key.into(),
        fields: Some(json_to_struct(fields)),
    }
}

pub fn fact_ref(typ: impl Into<String>, key: impl Into<String>) -> FactRef {
    FactRef {
        r#type: typ.into(),
        key: key.into(),
    }
}

fn json_to_proto(value: JsonValue) -> Value {
    let kind = match value {
        JsonValue::Null => Kind::NullValue(0),
        JsonValue::Bool(v) => Kind::BoolValue(v),
        JsonValue::Number(v) => Kind::NumberValue(v.as_f64().expect("number must fit f64")),
        JsonValue::String(v) => Kind::StringValue(v),
        JsonValue::Array(values) => Kind::ListValue(prost_types::ListValue {
            values: values.into_iter().map(json_to_proto).collect(),
        }),
        JsonValue::Object(values) => Kind::StructValue(Struct {
            fields: values.into_iter().map(|(k, v)| (k, json_to_proto(v))).collect(),
        }),
    };
    Value { kind: Some(kind) }
}
