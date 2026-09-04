//! Builds a GraphQL schema dynamically from a `MetadataRegistry` — the GraphQL counterpart to
//! `metap-metadata/src/openapi.rs`'s runtime JSON-Schema generation, using `async-graphql`'s
//! `dynamic` module (`async_graphql::dynamic::*`) specifically because entities are `EntityDefinition`
//! values discovered at *runtime*, not Rust types known at compile time — the same reason
//! `openapi.rs` can't just `#[derive(JsonSchema)]` on anything.
//!
//! **Field-level permission masking needs no code here at all.** Every resolver reads from a
//! `RecordHandle` built from an already-`filter_readable_fields`-masked `RecordDto` — the exact
//! same masking REST already runs inside `CrudService`. A denied field's key is simply absent
//! from the underlying JSON, and every resolver already treats a missing key as `null` (see
//! `record_handle.rs`'s doc comment). Because `Reference` fields resolve through the same
//! `CrudService::get`/`get_many` methods (via `RecordLoader`), this masking recurses through
//! nested queries for free too — there is no separate "mask at every nesting level" code path to
//! write or forget.
//!
//! **Complexity/depth limits** are `SchemaBuilder::limit_complexity`/`limit_depth`, both built
//! into `async-graphql`'s dynamic schema builder — set from caller-supplied config (a per-
//! deployment tuning knob), not hardcoded here.

use std::sync::Arc;

use async_graphql::dataloader::DataLoader;
use async_graphql::dynamic::{
    Field, FieldFuture, FieldValue, InputValue, Object, ResolverContext, Schema, SchemaBuilder, SchemaError, TypeRef,
};
use async_graphql::{Error as GqlError, Value as GqlValue};
use metap_crud::{RecordBackend, ServiceResult};
use metap_metadata::{FieldKind, MetadataRegistry};
use metap_permission::RequestContext;
use uuid::Uuid;

use crate::list_input::list_input_from_args;
use crate::loader::RecordLoader;
use crate::naming;
use crate::record_handle::RecordHandle;
use crate::type_map::{scalar_type_ref, JSON_SCALAR};

#[derive(Clone, Copy)]
pub struct SchemaLimits {
    pub depth: usize,
    pub complexity: usize,
}

impl Default for SchemaLimits {
    /// Starting guardrails, not a permanent tuning — see `docs/multi-tenant-platform-design.md`
    /// §10's "giữ guardrail 3s" framing; real numbers should be tuned against real query shapes
    /// once a downstream consumer has some.
    fn default() -> Self {
        Self {
            depth: 10,
            complexity: 1000,
        }
    }
}

/// Turns a `ServiceResult::Err` into a GraphQL error that a client can actually branch on.
///
/// Audit 04 finding B#2 (fixed 2026-09-03): this used to flatten everything into one string,
/// `format!("{status}: {message}")`, dropping the stable `error` code and the whole `field_errors`
/// map — so a form submitted through GraphQL learned that validation failed but never which field.
/// GraphQL's own answer to this is `extensions`, so the envelope goes there (`code`/`status`/
/// `fieldErrors`, same camelCase keys as the REST error body and as `metap-grpc`'s `ErrorDetails`,
/// so one client-side error handler covers all three transports). `message` stays the plain human
/// text it always was — no longer prefixed with a number a client had to parse back out.
fn service_result_to_gql<T>(result: ServiceResult<T>) -> Result<T, GqlError> {
    match result {
        ServiceResult::Ok { data, .. } => Ok(data),
        ServiceResult::Err {
            status,
            error,
            message,
            field_errors,
        } => {
            let mut err = GqlError::new(message.unwrap_or_else(|| error.clone()));
            let mut extensions = async_graphql::ErrorExtensionValues::default();
            extensions.set("code", error);
            extensions.set("status", status as i32);
            if let Some(field_errors) = field_errors {
                if let Ok(value) = async_graphql::Value::from_json(serde_json::json!(field_errors)) {
                    extensions.set("fieldErrors", value);
                }
            }
            err.extensions = Some(extensions);
            Err(err)
        }
    }
}

fn json_field_value(value: Option<&serde_json::Value>) -> Option<FieldValue<'static>> {
    match value {
        None | Some(serde_json::Value::Null) => None,
        Some(v) => GqlValue::from_json(v.clone()).ok().map(FieldValue::value),
    }
}

fn backend_from_ctx<'a>(ctx: &ResolverContext<'a>) -> &'a Arc<dyn RecordBackend> {
    ctx.data_unchecked::<Arc<dyn RecordBackend>>()
}

fn request_context_from_ctx<'a>(ctx: &ResolverContext<'a>) -> Result<&'a RequestContext, GqlError> {
    ctx.data::<RequestContext>()
}

/// Registers `id`/`entity`/`code`/`status`/`version`/`createdAt`/`updatedAt` — the fixed
/// `RecordDto` envelope every entity's `Object` type carries regardless of its own fields.
fn add_envelope_fields(mut obj: Object) -> Object {
    for key in ["id", "entity", "code", "status", "createdAt", "updatedAt"] {
        obj = obj.field(Field::new(key, TypeRef::named(TypeRef::STRING), move |ctx| {
            let handle = ctx
                .parent_value
                .downcast_ref::<RecordHandle>()
                .expect("parent_value is always a RecordHandle for entity object fields");
            FieldFuture::Value(json_field_value(handle.top_level(key)))
        }));
    }
    obj = obj.field(Field::new("version", TypeRef::named(TypeRef::INT), |ctx| {
        let handle = ctx
            .parent_value
            .downcast_ref::<RecordHandle>()
            .expect("parent_value is always a RecordHandle for entity object fields");
        FieldFuture::Value(json_field_value(handle.top_level("version")))
    }));
    obj = obj.field(Field::new("capabilities", TypeRef::named(JSON_SCALAR), |ctx| {
        let handle = ctx
            .parent_value
            .downcast_ref::<RecordHandle>()
            .expect("parent_value is always a RecordHandle for entity object fields");
        FieldFuture::Value(json_field_value(handle.top_level("capabilities")))
    }));
    obj
}

/// One `Object` type per entity, `data` fields typed per `FieldKind` (see `type_map.rs`).
/// `Reference` fields resolve through `RecordLoader` (the schema-wide `DataLoader`, per-request
/// instance — see `build_schema`'s wiring) instead of a direct `CrudService::get` call, so
/// several `Reference` fields resolved in one query batch into as few `get_many` calls as
/// possible.
fn build_entity_object(metadata: &MetadataRegistry, entity_name: &str) -> Object {
    let summary = metadata
        .get_entity_metadata(entity_name)
        .unwrap_or_else(|| panic!("entity {entity_name} must exist in the registry it was enumerated from"));
    let mut obj = Object::new(naming::type_name(entity_name));
    obj = add_envelope_fields(obj);

    // `EntityWorkflow.state_field` is conventionally *also* declared as a regular field in
    // `entity.fields` (an `Enum`, e.g. "status") — `add_envelope_fields` already registered a
    // `status` resolver reading the same value from `RecordDto`'s own top-level mirror column
    // (see `crates/metap-crud/src/crud_service/helpers.rs`'s `mask_record_for_read` doc comment
    // on why that mirror exists). Adding it again here would panic (`Object::field` asserts
    // against duplicate names) — skip any field whose name collides with an envelope field.
    const ENVELOPE_FIELD_NAMES: [&str; 8] = [
        "id",
        "entity",
        "code",
        "status",
        "createdAt",
        "updatedAt",
        "version",
        "capabilities",
    ];

    for field in summary.fields {
        if ENVELOPE_FIELD_NAMES.contains(&field.name.as_str()) {
            continue;
        }
        let field_name = field.name.clone();
        match field.kind {
            FieldKind::Reference => {
                let Some(ref_entity_name) = field.ref_entity.clone() else {
                    continue; // malformed metadata — `MetadataCompiler` should already reject this at register()
                };
                let ref_type_name = naming::type_name(&ref_entity_name);
                obj = obj.field(Field::new(
                    field.name.clone(),
                    TypeRef::named(ref_type_name),
                    move |ctx| {
                        let field_name = field_name.clone();
                        let ref_entity_name = ref_entity_name.clone();
                        FieldFuture::new(async move {
                            let handle = ctx
                                .parent_value
                                .downcast_ref::<RecordHandle>()
                                .expect("parent_value is always a RecordHandle for entity object fields");
                            let Some(id_str) = handle.data_field(&field_name).and_then(|v| v.as_str()) else {
                                return Ok(None);
                            };
                            let Ok(id) = Uuid::parse_str(id_str) else {
                                return Ok(None);
                            };
                            let loader = ctx.data_unchecked::<DataLoader<RecordLoader>>();
                            let loaded = loader
                                .load_one((ref_entity_name, id))
                                .await
                                .map_err(|e| GqlError::new(e.to_string()))?;
                            Ok(loaded.map(|(dto, capabilities)| {
                                FieldValue::owned_any(RecordHandle::from_dto_with_capabilities(dto, capabilities))
                            }))
                        })
                    },
                ));
            }
            other_kind => {
                let type_ref = scalar_type_ref(other_kind, None);
                obj = obj.field(Field::new(field.name.clone(), type_ref, move |ctx| {
                    let handle = ctx
                        .parent_value
                        .downcast_ref::<RecordHandle>()
                        .expect("parent_value is always a RecordHandle for entity object fields");
                    FieldFuture::Value(json_field_value(handle.data_field(&field_name)))
                }));
            }
        }
    }
    obj
}

/// `{Type}Connection` — `records`/`nextCursor`/`hasMore`, mirroring `metap-grpc`'s `ListResponse`
/// shape so the two non-REST transports agree on what a list result looks like. All three fields
/// read from the same `ConnectionHandle` parent value.
fn build_connection_object(entity_name: &str) -> Object {
    let type_name = naming::type_name(entity_name);
    Object::new(naming::connection_type_name(entity_name))
        .field(Field::new("records", TypeRef::named_nn_list_nn(&type_name), |ctx| {
            let handle = ctx
                .parent_value
                .downcast_ref::<ConnectionHandle>()
                .expect("parent_value is always a ConnectionHandle for a Connection's fields");
            FieldFuture::Value(Some(FieldValue::list(
                handle.records.iter().cloned().map(FieldValue::owned_any),
            )))
        }))
        .field(Field::new("nextCursor", TypeRef::named(TypeRef::STRING), |ctx| {
            let handle = ctx
                .parent_value
                .downcast_ref::<ConnectionHandle>()
                .expect("parent_value is always a ConnectionHandle for a Connection's fields");
            FieldFuture::Value(handle.next_cursor.clone().map(GqlValue::from).map(FieldValue::value))
        }))
        .field(Field::new("hasMore", TypeRef::named_nn(TypeRef::BOOLEAN), |ctx| {
            let handle = ctx
                .parent_value
                .downcast_ref::<ConnectionHandle>()
                .expect("parent_value is always a ConnectionHandle for a Connection's fields");
            FieldFuture::Value(Some(FieldValue::value(GqlValue::from(handle.has_more))))
        }))
}

#[derive(Clone)]
struct ConnectionHandle {
    records: Vec<RecordHandle>,
    next_cursor: Option<String>,
    has_more: bool,
}

fn add_query_fields(mut query: Object, entity_name: &str, type_name: &str, connection_type_name: &str) -> Object {
    let get_entity_name = entity_name.to_string();
    query = query.field(
        Field::new(
            naming::get_field_name(entity_name),
            TypeRef::named(type_name),
            move |ctx| {
                let entity_name = get_entity_name.clone();
                FieldFuture::new(async move {
                    let backend = backend_from_ctx(&ctx);
                    let context = request_context_from_ctx(&ctx)?;
                    let id =
                        Uuid::parse_str(ctx.args.try_get("id")?.string()?).map_err(|e| GqlError::new(e.to_string()))?;
                    let result = backend
                        .get(&entity_name, id, context)
                        .await
                        .map_err(|e| GqlError::new(e.to_string()))?;
                    match result {
                        ServiceResult::Ok {
                            data: (dto, capabilities),
                            ..
                        } => Ok(Some(FieldValue::owned_any(RecordHandle::from_dto_with_capabilities(
                            dto,
                            capabilities,
                        )))),
                        ServiceResult::Err {
                            status, error, message, ..
                        } => Err(GqlError::new(format!("{status}: {}", message.unwrap_or(error)))),
                    }
                })
            },
        )
        .argument(InputValue::new("id", TypeRef::named_nn(TypeRef::ID))),
    );

    let list_entity_name = entity_name.to_string();
    query = query.field(
        Field::new(
            naming::list_field_name(entity_name),
            TypeRef::named_nn(connection_type_name),
            move |ctx| {
                let entity_name = list_entity_name.clone();
                FieldFuture::new(async move {
                    let backend = backend_from_ctx(&ctx);
                    let context = request_context_from_ctx(&ctx)?;
                    let input = list_input_from_args(&ctx.args)?;
                    let result = backend
                        .list(&entity_name, &input, context)
                        .await
                        .map_err(|e| GqlError::new(e.to_string()))?;
                    match result {
                        ServiceResult::Ok { data, page } => {
                            let records = data.into_iter().map(RecordHandle::from_dto).collect();
                            let next_cursor = page.as_ref().and_then(|p| p.next_cursor.clone());
                            let has_more = page.as_ref().map(|p| p.next_cursor.is_some()).unwrap_or(false);
                            Ok(Some(FieldValue::owned_any(ConnectionHandle {
                                records,
                                next_cursor,
                                has_more,
                            })))
                        }
                        ServiceResult::Err {
                            status, error, message, ..
                        } => Err(GqlError::new(format!("{status}: {}", message.unwrap_or(error)))),
                    }
                })
            },
        )
        .argument(InputValue::new("filter", TypeRef::named(JSON_SCALAR)))
        .argument(InputValue::new("sort", TypeRef::named(TypeRef::STRING)))
        .argument(InputValue::new("cursor", TypeRef::named(TypeRef::STRING)))
        .argument(InputValue::new("limit", TypeRef::named(TypeRef::INT)))
        .argument(InputValue::new("listView", TypeRef::named(TypeRef::STRING))),
    );

    query
}

fn add_mutation_fields(mut mutation: Object, entity_name: &str, type_name: &str, has_workflow: bool) -> Object {
    let create_entity_name = entity_name.to_string();
    mutation = mutation.field(
        Field::new(
            naming::create_field_name(entity_name),
            TypeRef::named(type_name),
            move |ctx| {
                let entity_name = create_entity_name.clone();
                FieldFuture::new(async move {
                    let backend = backend_from_ctx(&ctx);
                    let context = request_context_from_ctx(&ctx)?;
                    let data =
                        json_object_arg(&ctx, "data")?.ok_or_else(|| GqlError::new("`data` must be an object"))?;
                    let result = backend
                        .create(&entity_name, &data, context)
                        .await
                        .map_err(|e| GqlError::new(e.to_string()))?;
                    let dto = service_result_to_gql(result)?;
                    Ok(Some(FieldValue::owned_any(RecordHandle::from_dto(dto))))
                })
            },
        )
        .argument(InputValue::new("data", TypeRef::named_nn(JSON_SCALAR))),
    );

    let update_entity_name = entity_name.to_string();
    mutation = mutation.field(
        Field::new(
            naming::update_field_name(entity_name),
            TypeRef::named(type_name),
            move |ctx| {
                let entity_name = update_entity_name.clone();
                FieldFuture::new(async move {
                    let backend = backend_from_ctx(&ctx);
                    let context = request_context_from_ctx(&ctx)?;
                    let id =
                        Uuid::parse_str(ctx.args.try_get("id")?.string()?).map_err(|e| GqlError::new(e.to_string()))?;
                    let expected_version = ctx.args.try_get("expectedVersion")?.i64()? as i32;
                    let data =
                        json_object_arg(&ctx, "data")?.ok_or_else(|| GqlError::new("`data` must be an object"))?;
                    let result = backend
                        .update(&entity_name, id, expected_version, &data, context)
                        .await
                        .map_err(|e| GqlError::new(e.to_string()))?;
                    let dto = service_result_to_gql(result)?;
                    Ok(Some(FieldValue::owned_any(RecordHandle::from_dto(dto))))
                })
            },
        )
        .argument(InputValue::new("id", TypeRef::named_nn(TypeRef::ID)))
        .argument(InputValue::new("expectedVersion", TypeRef::named_nn(TypeRef::INT)))
        .argument(InputValue::new("data", TypeRef::named_nn(JSON_SCALAR))),
    );

    let delete_entity_name = entity_name.to_string();
    mutation = mutation.field(
        Field::new(
            naming::delete_field_name(entity_name),
            TypeRef::named(type_name),
            move |ctx| {
                let entity_name = delete_entity_name.clone();
                FieldFuture::new(async move {
                    let backend = backend_from_ctx(&ctx);
                    let context = request_context_from_ctx(&ctx)?;
                    let id =
                        Uuid::parse_str(ctx.args.try_get("id")?.string()?).map_err(|e| GqlError::new(e.to_string()))?;
                    let expected_version = ctx.args.try_get("expectedVersion")?.i64()? as i32;
                    let result = backend
                        .delete(&entity_name, id, expected_version, context)
                        .await
                        .map_err(|e| GqlError::new(e.to_string()))?;
                    let dto = service_result_to_gql(result)?;
                    Ok(Some(FieldValue::owned_any(RecordHandle::from_dto(dto))))
                })
            },
        )
        .argument(InputValue::new("id", TypeRef::named_nn(TypeRef::ID)))
        .argument(InputValue::new("expectedVersion", TypeRef::named_nn(TypeRef::INT))),
    );

    if has_workflow {
        let transition_entity_name = entity_name.to_string();
        mutation = mutation.field(
            Field::new(
                naming::transition_field_name(entity_name),
                TypeRef::named(type_name),
                move |ctx| {
                    let entity_name = transition_entity_name.clone();
                    FieldFuture::new(async move {
                        let backend = backend_from_ctx(&ctx);
                        let context = request_context_from_ctx(&ctx)?;
                        let id = Uuid::parse_str(ctx.args.try_get("id")?.string()?)
                            .map_err(|e| GqlError::new(e.to_string()))?;
                        let action = ctx.args.try_get("action")?.string()?.to_string();
                        let expected_version = ctx.args.try_get("expectedVersion")?.i64()? as i32;
                        let data = json_object_arg(&ctx, "data")?;
                        let result = backend
                            .transition(&entity_name, id, &action, expected_version, data.as_ref(), context)
                            .await
                            .map_err(|e| GqlError::new(e.to_string()))?;
                        let dto = service_result_to_gql(result)?;
                        Ok(Some(FieldValue::owned_any(RecordHandle::from_dto(dto))))
                    })
                },
            )
            .argument(InputValue::new("id", TypeRef::named_nn(TypeRef::ID)))
            .argument(InputValue::new("action", TypeRef::named_nn(TypeRef::STRING)))
            .argument(InputValue::new("expectedVersion", TypeRef::named_nn(TypeRef::INT)))
            .argument(InputValue::new("data", TypeRef::named(JSON_SCALAR))),
        );
    }

    mutation
}

/// Reads a `Json`-scalar argument and requires it to be a JSON object (every mutation's `data`
/// argument — `CrudService::create/update/transition` all take a `JsonObject`, never a bare
/// scalar/array). `None` for an absent/null argument (the `transition` mutation's `data` is
/// optional, unlike `create`/`update`'s).
fn json_object_arg(ctx: &ResolverContext<'_>, name: &str) -> Result<Option<metap_crud::JsonObject>, GqlError> {
    match ctx.args.get(name) {
        Some(v) if !v.is_null() => {
            let json = v
                .as_value()
                .clone()
                .into_json()
                .map_err(|e| GqlError::new(e.to_string()))?;
            Ok(json.as_object().cloned())
        }
        _ => Ok(None),
    }
}

/// Builds every piece of the generic entity schema — one `Object` type + one `{Type}Connection`
/// type per entity registered onto `builder`, and a `Query`/`Mutation` root each carrying
/// `get`/`list`/`create`/`update`/`delete`(/`transition`) fields per entity — but stops short of
/// `.finish()`, so a caller that needs fields beyond generic entity CRUD (e.g. a downstream
/// binary's own hand-written resolvers for an endpoint `metap-graphql` has no way to synthesize,
/// same reason `metap-http::build_router` takes an `extra_routes` router) can add its own
/// `.field(...)` calls onto the returned `query`/`mutation` objects before registering and
/// finishing the schema itself. [`build_schema`] is the common-case thin wrapper that finishes
/// immediately with no extra fields.
///
/// `backend` becomes schema-wide data (`ctx.data_unchecked::<Arc<dyn RecordBackend>>()`) — stable
/// for the schema's lifetime, unlike `RequestContext`/the `DataLoader`, which are per-request data
/// a caller (`metap-graphql-http`) attaches via [`with_request_data`]. Passing an
/// `Arc<dyn RecordBackend>` rather than `Arc<CrudService>` is what lets the same resolver code
/// serve both a single-service binary (`Arc::new(crud) as Arc<dyn RecordBackend>`) and the BFF
/// gateway (`crates/graphql-gateway`'s `CompositeBackend`, routing per entity to a remote
/// `GrpcBackend`) without any resolver knowing which one it got.
pub fn build_schema_parts(
    metadata: &MetadataRegistry,
    backend: Arc<dyn RecordBackend>,
    limits: SchemaLimits,
) -> (SchemaBuilder, Object, Object) {
    let entities = metadata.list_entities();

    let mut query = Object::new("Query");
    let mut mutation = Object::new("Mutation");
    let mut builder: SchemaBuilder = Schema::build("Query", Some("Mutation"), None)
        .data(backend)
        .register(async_graphql::dynamic::Scalar::new(JSON_SCALAR))
        .limit_depth(limits.depth)
        .limit_complexity(limits.complexity);

    for summary in &entities {
        let entity_name = &summary.name;
        let type_name = naming::type_name(entity_name);
        let connection_type_name = naming::connection_type_name(entity_name);

        builder = builder
            .register(build_entity_object(metadata, entity_name))
            .register(build_connection_object(entity_name));

        query = add_query_fields(query, entity_name, &type_name, &connection_type_name);
        mutation = add_mutation_fields(mutation, entity_name, &type_name, summary.workflow.is_some());
    }

    (builder, query, mutation)
}

/// Builds the full schema with no fields beyond generic entity CRUD — see [`build_schema_parts`]
/// for the extension point a caller with its own custom resolvers needs instead.
pub fn build_schema(
    metadata: &MetadataRegistry,
    backend: Arc<dyn RecordBackend>,
    limits: SchemaLimits,
) -> Result<Schema, SchemaError> {
    let (builder, query, mutation) = build_schema_parts(metadata, backend, limits);
    builder.register(query).register(mutation).finish()
}

/// Attaches the two pieces of per-request state every resolver needs beyond schema-wide data:
/// the caller's `RequestContext` (permission/tenant scoping — every `CrudService` call needs
/// one) and a fresh `DataLoader<RecordLoader>` (so `Reference` field batching never leaks across
/// requests/tenants — see `loader.rs`'s doc comment for why a per-request instance is required,
/// not a schema-wide one). `metap-graphql-http` calls this on the `async_graphql::Request`
/// `async-graphql-axum`'s `GraphQLRequest` extractor already parsed from the HTTP body, rather
/// than parsing the query itself here.
pub fn with_request_data(
    request: async_graphql::Request,
    backend: Arc<dyn RecordBackend>,
    context: RequestContext,
) -> async_graphql::Request {
    let loader = DataLoader::new(
        RecordLoader {
            backend,
            context: context.clone(),
        },
        tokio::spawn,
    );
    request.data(context).data(loader)
}
