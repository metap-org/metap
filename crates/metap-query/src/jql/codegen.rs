//! AST -> SQL fragment, validated against the entity's own metadata (`super`'s doc comment):
//! field names checked against `entity.fields`, operators gated by `FieldKind`, every value
//! bound as a parameter — nothing from the query text is ever interpolated into the SQL string.

use metap_metadata::{field_has_real_column, EntityDefinition, FieldKind};

use crate::sql_builder::{BindValue, ParamBuilder};

use super::ast::{CompareOp, JqlExpr, JqlValue};
use super::lexer::{err, tokenize, InvalidJqlError};
use super::parser::Parser;

#[derive(Clone, Copy)]
enum JqlFieldType {
    Text,
    Number,
    Money,
    Boolean,
    Date,
    Datetime,
    Uuid,
}

fn cast_suffix(t: JqlFieldType) -> &'static str {
    match t {
        JqlFieldType::Text => "",
        JqlFieldType::Number => "::double precision",
        JqlFieldType::Money => "::numeric",
        JqlFieldType::Boolean => "::boolean",
        JqlFieldType::Date => "::date",
        JqlFieldType::Datetime => "::timestamptz",
        JqlFieldType::Uuid => "::uuid",
    }
}

/// Field name -> (SQL expression yielding that field's value, its comparison type). `createdAt`/
/// `updatedAt` are real columns on every table (generic or dedicated) so they resolve first,
/// same special case `query_planner::sort_field_expression` carries. Everything else must be a
/// real entry in `entity.fields` — an unknown name is the one thing this whole module treats as
/// a hard error instead of the "silently ignore" convention `plan_list`'s plain query-param
/// filters use, since a JQL typo the caller can't see any other way deserves a visible error.
fn resolve_field(
    field_name: &str,
    entity: &EntityDefinition,
    dedicated_table: bool,
    params: &mut ParamBuilder,
) -> Result<(String, JqlFieldType), InvalidJqlError> {
    match field_name {
        "createdAt" => return Ok(("created_at".to_string(), JqlFieldType::Datetime)),
        "updatedAt" => return Ok(("updated_at".to_string(), JqlFieldType::Datetime)),
        _ => {}
    }
    let Some(field) = entity.fields.iter().find(|f| f.name == field_name) else {
        return err(format!("Unknown field: {field_name}"));
    };
    let ftype = match field.kind {
        FieldKind::String | FieldKind::Enum | FieldKind::Json => JqlFieldType::Text,
        FieldKind::Number => JqlFieldType::Number,
        FieldKind::Money => JqlFieldType::Money,
        FieldKind::Boolean => JqlFieldType::Boolean,
        FieldKind::Date => JqlFieldType::Date,
        FieldKind::Datetime => JqlFieldType::Datetime,
        FieldKind::Reference | FieldKind::Id => JqlFieldType::Uuid,
    };
    if dedicated_table && field_has_real_column(field) {
        return Ok((format!("\"{field_name}\""), ftype));
    }
    let ph = params.push(BindValue::Text(field_name.to_string()));
    Ok((format!("jsonb_extract_path_text(data, {ph})"), ftype))
}

fn validate_op(op: CompareOp, ftype: JqlFieldType, field: &str) -> Result<(), InvalidJqlError> {
    let ok = match op {
        CompareOp::Eq | CompareOp::Ne => true,
        CompareOp::Gt | CompareOp::Gte | CompareOp::Lt | CompareOp::Lte => {
            matches!(
                ftype,
                JqlFieldType::Number | JqlFieldType::Money | JqlFieldType::Date | JqlFieldType::Datetime
            )
        }
        CompareOp::Contains | CompareOp::NotContains => matches!(ftype, JqlFieldType::Text),
    };
    if ok {
        Ok(())
    } else {
        err(format!("Operator not supported on field `{field}`"))
    }
}

fn value_to_bind_text(value: &JqlValue) -> String {
    match value {
        JqlValue::Str(s) => s.clone(),
        JqlValue::Bool(b) => b.to_string(),
    }
}

/// Same escaping `query_planner::plan_list`'s substring-search branch uses for a `searchable`
/// field's ILIKE pattern — kept in sync deliberately (a `~` value must not let `%`/`_` act as
/// SQL wildcards just because the caller typed them).
fn ilike_pattern(value: &str) -> String {
    let escaped: String = value
        .chars()
        .flat_map(|c| {
            if matches!(c, '\\' | '%' | '_') {
                vec!['\\', c]
            } else {
                vec![c]
            }
        })
        .collect();
    format!("%{escaped}%")
}

fn compile_expr(
    expr: &JqlExpr,
    entity: &EntityDefinition,
    dedicated_table: bool,
    params: &mut ParamBuilder,
) -> Result<String, InvalidJqlError> {
    match expr {
        JqlExpr::And(parts) => {
            let sql: Result<Vec<String>, _> = parts
                .iter()
                .map(|e| compile_expr(e, entity, dedicated_table, params))
                .collect();
            Ok(format!("({})", sql?.join(" AND ")))
        }
        JqlExpr::Or(parts) => {
            let sql: Result<Vec<String>, _> = parts
                .iter()
                .map(|e| compile_expr(e, entity, dedicated_table, params))
                .collect();
            Ok(format!("({})", sql?.join(" OR ")))
        }
        JqlExpr::Not(inner) => {
            let sql = compile_expr(inner, entity, dedicated_table, params)?;
            Ok(format!("(NOT {sql})"))
        }
        JqlExpr::IsEmpty { field, negate } => {
            let (base, ftype) = resolve_field(field, entity, dedicated_table, params)?;
            let lhs = format!("({base}){}", cast_suffix(ftype));
            Ok(if *negate {
                format!("({lhs} IS NOT NULL)")
            } else {
                format!("({lhs} IS NULL)")
            })
        }
        JqlExpr::Compare { field, op, value } => {
            let (base, ftype) = resolve_field(field, entity, dedicated_table, params)?;
            validate_op(*op, ftype, field)?;
            let lhs = format!("({base}){}", cast_suffix(ftype));
            if matches!(op, CompareOp::Contains | CompareOp::NotContains) {
                let text = match value {
                    JqlValue::Str(s) => s.clone(),
                    JqlValue::Bool(_) => return err(format!("`~`/`!~` need a string value for field `{field}`")),
                };
                let ph = params.push(BindValue::Text(ilike_pattern(&text)));
                return Ok(if matches!(op, CompareOp::Contains) {
                    format!("({lhs} ILIKE {ph})")
                } else {
                    format!("({lhs} NOT ILIKE {ph})")
                });
            }
            let ph = params.push(BindValue::Text(value_to_bind_text(value)));
            let cast = cast_suffix(ftype);
            let sql_op = match op {
                CompareOp::Eq => "=",
                CompareOp::Ne => "!=",
                CompareOp::Gt => ">",
                CompareOp::Gte => ">=",
                CompareOp::Lt => "<",
                CompareOp::Lte => "<=",
                CompareOp::Contains | CompareOp::NotContains => unreachable!(),
            };
            Ok(format!("({lhs} {sql_op} {ph}{cast})"))
        }
        JqlExpr::In { field, negate, values } => {
            if values.is_empty() {
                return err(format!("`IN`/`NOT IN` needs at least one value for field `{field}`"));
            }
            let (base, ftype) = resolve_field(field, entity, dedicated_table, params)?;
            let lhs = format!("({base}){}", cast_suffix(ftype));
            let cast = cast_suffix(ftype);
            let mut placeholders = Vec::with_capacity(values.len());
            for v in values {
                let ph = params.push(BindValue::Text(value_to_bind_text(v)));
                placeholders.push(format!("{ph}{cast}"));
            }
            let sql_op = if *negate { "NOT IN" } else { "IN" };
            Ok(format!("({lhs} {sql_op} ({}))", placeholders.join(", ")))
        }
    }
}

fn is_sortable(field_name: &str, entity: &EntityDefinition) -> bool {
    field_name == "createdAt"
        || field_name == "updatedAt"
        || entity
            .fields
            .iter()
            .any(|f| f.name == field_name && f.sortable.unwrap_or(false))
}

/// `(WHERE fragment, (order-by field, descending))` — see `parse_and_compile_jql`'s doc comment.
pub type JqlCompileResult = Result<(Option<String>, Option<(String, bool)>), InvalidJqlError>;

/// Parses and compiles a JQL string against one entity. Returns the compiled `WHERE` fragment
/// (already fully parenthesized, ready to `AND` into `plan_list`'s other conditions — `None` if
/// the query was only an `ORDER BY` with no filter expression) plus an optional `(field,
/// descending)` pair for its `ORDER BY` clause, in the same shape `plan_list`'s own `-field`
/// sort-string convention already uses, so the caller can fold it into the existing single-sort
/// resolution instead of this module reimplementing sort/cursor handling.
pub fn parse_and_compile_jql(
    jql: &str,
    entity: &EntityDefinition,
    dedicated_table: bool,
    params: &mut ParamBuilder,
) -> JqlCompileResult {
    let tokens = tokenize(jql)?;
    let mut parser = Parser::new(tokens);
    let parsed = parser.parse_query()?;

    let where_sql = parsed
        .expr
        .as_ref()
        .map(|e| compile_expr(e, entity, dedicated_table, params))
        .transpose()?;

    let order_by = match parsed.order_by {
        Some(o) if is_sortable(&o.field, entity) => Some((o.field, o.descending)),
        Some(o) => return err(format!("Field `{}` is not sortable", o.field)),
        None => None,
    };

    Ok((where_sql, order_by))
}
