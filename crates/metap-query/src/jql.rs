//! A small, generic query language ("JQL", after Jira's) any entity can use through
//! `ListInput.jql` — `field OP value` comparisons combined with `AND`/`OR`/`NOT`, e.g.
//! `priority = "high" AND status != "done" ORDER BY dueDate DESC`. Lives in `metap-query`
//! (the crate that already owns `plan_list`/`QueryPlanner`, this project's one deliberate
//! Postgres-dialect seam — see this crate's top doc comment) rather than any app — nothing
//! here is jira-specific, it only ever sees the calling entity's own `EntityDefinition`.
//!
//! Same security posture the boundary in `docs/architectures/05-building-blocks.md` requires of
//! every other filter path: **field names are validated against `entity.fields`** (never an
//! arbitrary client-supplied column/JSON path), **operators are a fixed allowlist** gated by the
//! field's `FieldKind` (no `>`/`<` on a `String`, no `~` on a `Boolean`), and **every value is
//! bound as a SQL parameter** (`ParamBuilder`/`BindValue`) — nothing from the query text is ever
//! interpolated directly into the generated SQL string. A malformed or disallowed query is a
//! clean `InvalidJqlError` (→ HTTP 400 via `CrudService::list`'s downcast, same pattern as
//! `InvalidCursorError`/`UnknownListViewError`), never a panic or a raw DB error.
//!
//! Deliberately smaller than real JQL: string/number/`true`/`false` literals only (no bare
//! unquoted words — `status = Done` isn't valid, write `status = "Done"`; keeps the lexer from
//! having to disambiguate a bare value from a field name), and `ORDER BY` takes exactly one
//! field — the rest of this codebase (cursor pagination, `plan_list`'s `ResolvedSort`) only
//! ever models a single sort column, so a multi-column `ORDER BY` would be accepted here and
//! silently unusable everywhere else.

use metap_metadata::{field_has_real_column, EntityDefinition, FieldKind};

use crate::sql_builder::{BindValue, ParamBuilder};

#[derive(Debug)]
pub struct InvalidJqlError(pub String);

impl std::fmt::Display for InvalidJqlError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}
impl std::error::Error for InvalidJqlError {}

fn err<T>(msg: impl Into<String>) -> Result<T, InvalidJqlError> {
    Err(InvalidJqlError(msg.into()))
}

// ---------------------------------------------------------------------------------------------
// Lexer
// ---------------------------------------------------------------------------------------------

#[derive(Debug, Clone, PartialEq)]
enum Token {
    Ident(String),
    Str(String),
    Number(String),
    LParen,
    RParen,
    Comma,
    Eq,
    Ne,
    Gt,
    Gte,
    Lt,
    Lte,
    Tilde,
    NotTilde,
    KwAnd,
    KwOr,
    KwNot,
    KwIn,
    KwIs,
    KwEmpty,
    KwOrder,
    KwBy,
    KwAsc,
    KwDesc,
    KwTrue,
    KwFalse,
    Eof,
}

fn keyword(word: &str) -> Option<Token> {
    Some(match word.to_ascii_uppercase().as_str() {
        "AND" => Token::KwAnd,
        "OR" => Token::KwOr,
        "NOT" => Token::KwNot,
        "IN" => Token::KwIn,
        "IS" => Token::KwIs,
        "EMPTY" => Token::KwEmpty,
        "ORDER" => Token::KwOrder,
        "BY" => Token::KwBy,
        "ASC" => Token::KwAsc,
        "DESC" => Token::KwDesc,
        "TRUE" => Token::KwTrue,
        "FALSE" => Token::KwFalse,
        _ => return None,
    })
}

/// Max input length — a generous ceiling against pathological/abusive input (e.g. thousands of
/// `NOT NOT NOT ...`) reaching the recursive-descent parser at all, same defensive posture as
/// `ListInput.limit`'s `<= 200` cap.
const MAX_JQL_LEN: usize = 2000;

fn tokenize(input: &str) -> Result<Vec<Token>, InvalidJqlError> {
    if input.len() > MAX_JQL_LEN {
        return err(format!("Query too long (max {MAX_JQL_LEN} characters)"));
    }
    let chars: Vec<char> = input.chars().collect();
    let mut tokens = Vec::new();
    let mut i = 0;
    while i < chars.len() {
        let c = chars[i];
        if c.is_whitespace() {
            i += 1;
            continue;
        }
        match c {
            '(' => {
                tokens.push(Token::LParen);
                i += 1;
            }
            ')' => {
                tokens.push(Token::RParen);
                i += 1;
            }
            ',' => {
                tokens.push(Token::Comma);
                i += 1;
            }
            '=' => {
                tokens.push(Token::Eq);
                i += 1;
            }
            '!' => {
                if chars.get(i + 1) == Some(&'=') {
                    tokens.push(Token::Ne);
                    i += 2;
                } else if chars.get(i + 1) == Some(&'~') {
                    tokens.push(Token::NotTilde);
                    i += 2;
                } else {
                    return err("Unexpected '!' (expected '!=' or '!~')");
                }
            }
            '>' => {
                if chars.get(i + 1) == Some(&'=') {
                    tokens.push(Token::Gte);
                    i += 2;
                } else {
                    tokens.push(Token::Gt);
                    i += 1;
                }
            }
            '<' => {
                if chars.get(i + 1) == Some(&'=') {
                    tokens.push(Token::Lte);
                    i += 2;
                } else {
                    tokens.push(Token::Lt);
                    i += 1;
                }
            }
            '~' => {
                tokens.push(Token::Tilde);
                i += 1;
            }
            '\'' | '"' => {
                let quote = c;
                i += 1;
                let mut s = String::new();
                let mut closed = false;
                while i < chars.len() {
                    let ch = chars[i];
                    if ch == '\\' && i + 1 < chars.len() {
                        s.push(chars[i + 1]);
                        i += 2;
                        continue;
                    }
                    if ch == quote {
                        closed = true;
                        i += 1;
                        break;
                    }
                    s.push(ch);
                    i += 1;
                }
                if !closed {
                    return err("Unterminated string literal");
                }
                tokens.push(Token::Str(s));
            }
            _ if c.is_ascii_digit() || (c == '-' && chars.get(i + 1).is_some_and(|d| d.is_ascii_digit())) => {
                let start = i;
                i += 1;
                while i < chars.len() && (chars[i].is_ascii_digit() || chars[i] == '.') {
                    i += 1;
                }
                tokens.push(Token::Number(chars[start..i].iter().collect()));
            }
            _ if c.is_alphabetic() || c == '_' => {
                let start = i;
                while i < chars.len() && (chars[i].is_alphanumeric() || chars[i] == '_') {
                    i += 1;
                }
                let word: String = chars[start..i].iter().collect();
                tokens.push(keyword(&word).unwrap_or(Token::Ident(word)));
            }
            _ => return err(format!("Unexpected character '{c}'")),
        }
    }
    tokens.push(Token::Eof);
    Ok(tokens)
}

// ---------------------------------------------------------------------------------------------
// AST
// ---------------------------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq)]
enum CompareOp {
    Eq,
    Ne,
    Gt,
    Gte,
    Lt,
    Lte,
    Contains,
    NotContains,
}

#[derive(Debug, Clone)]
enum JqlValue {
    Str(String),
    Bool(bool),
}

#[derive(Debug, Clone)]
enum JqlExpr {
    Compare {
        field: String,
        op: CompareOp,
        value: JqlValue,
    },
    In {
        field: String,
        negate: bool,
        values: Vec<JqlValue>,
    },
    IsEmpty {
        field: String,
        negate: bool,
    },
    And(Vec<JqlExpr>),
    Or(Vec<JqlExpr>),
    Not(Box<JqlExpr>),
}

struct JqlOrder {
    field: String,
    descending: bool,
}

struct ParsedJql {
    expr: Option<JqlExpr>,
    order_by: Option<JqlOrder>,
}

// ---------------------------------------------------------------------------------------------
// Parser (recursive descent; OR lowest precedence, then AND, then NOT, then a comparison atom)
// ---------------------------------------------------------------------------------------------

struct Parser {
    tokens: Vec<Token>,
    pos: usize,
}

impl Parser {
    fn peek(&self) -> &Token {
        &self.tokens[self.pos]
    }

    fn advance(&mut self) -> Token {
        let t = self.tokens[self.pos].clone();
        if self.pos + 1 < self.tokens.len() {
            self.pos += 1;
        }
        t
    }

    fn eat(&mut self, t: &Token) -> bool {
        if self.peek() == t {
            self.advance();
            true
        } else {
            false
        }
    }

    fn expect(&mut self, t: &Token, what: &str) -> Result<(), InvalidJqlError> {
        if self.eat(t) {
            Ok(())
        } else {
            err(format!("Expected {what}"))
        }
    }

    fn parse_query(&mut self) -> Result<ParsedJql, InvalidJqlError> {
        let expr = if matches!(self.peek(), Token::KwOrder | Token::Eof) {
            None
        } else {
            Some(self.parse_or()?)
        };
        let order_by = if self.eat(&Token::KwOrder) {
            self.expect(&Token::KwBy, "`BY` after `ORDER`")?;
            let field = self.expect_ident()?;
            let descending = if self.eat(&Token::KwDesc) {
                true
            } else {
                self.eat(&Token::KwAsc);
                false
            };
            Some(JqlOrder { field, descending })
        } else {
            None
        };
        if self.peek() != &Token::Eof {
            return err("Unexpected trailing input");
        }
        Ok(ParsedJql { expr, order_by })
    }

    fn parse_or(&mut self) -> Result<JqlExpr, InvalidJqlError> {
        let mut parts = vec![self.parse_and()?];
        while self.eat(&Token::KwOr) {
            parts.push(self.parse_and()?);
        }
        Ok(if parts.len() == 1 {
            parts.remove(0)
        } else {
            JqlExpr::Or(parts)
        })
    }

    fn parse_and(&mut self) -> Result<JqlExpr, InvalidJqlError> {
        let mut parts = vec![self.parse_not()?];
        while self.eat(&Token::KwAnd) {
            parts.push(self.parse_not()?);
        }
        Ok(if parts.len() == 1 {
            parts.remove(0)
        } else {
            JqlExpr::And(parts)
        })
    }

    fn parse_not(&mut self) -> Result<JqlExpr, InvalidJqlError> {
        if self.eat(&Token::KwNot) {
            Ok(JqlExpr::Not(Box::new(self.parse_not()?)))
        } else {
            self.parse_primary()
        }
    }

    fn parse_primary(&mut self) -> Result<JqlExpr, InvalidJqlError> {
        if self.eat(&Token::LParen) {
            let e = self.parse_or()?;
            self.expect(&Token::RParen, "`)`")?;
            return Ok(e);
        }
        self.parse_comparison()
    }

    fn expect_ident(&mut self) -> Result<String, InvalidJqlError> {
        match self.advance() {
            Token::Ident(s) => Ok(s),
            _ => err("Expected a field name"),
        }
    }

    fn parse_value(&mut self) -> Result<JqlValue, InvalidJqlError> {
        match self.advance() {
            Token::Str(s) => Ok(JqlValue::Str(s)),
            Token::Number(n) => Ok(JqlValue::Str(n)),
            Token::KwTrue => Ok(JqlValue::Bool(true)),
            Token::KwFalse => Ok(JqlValue::Bool(false)),
            _ => err("Expected a value (string, number, true, or false)"),
        }
    }

    fn parse_value_list(&mut self) -> Result<Vec<JqlValue>, InvalidJqlError> {
        self.expect(&Token::LParen, "`(` after `IN`")?;
        let mut values = vec![self.parse_value()?];
        while self.eat(&Token::Comma) {
            values.push(self.parse_value()?);
        }
        self.expect(&Token::RParen, "`)` to close the value list")?;
        Ok(values)
    }

    fn parse_comparison(&mut self) -> Result<JqlExpr, InvalidJqlError> {
        let field = self.expect_ident()?;
        let expr = match self.advance() {
            Token::Eq => JqlExpr::Compare {
                field,
                op: CompareOp::Eq,
                value: self.parse_value()?,
            },
            Token::Ne => JqlExpr::Compare {
                field,
                op: CompareOp::Ne,
                value: self.parse_value()?,
            },
            Token::Gt => JqlExpr::Compare {
                field,
                op: CompareOp::Gt,
                value: self.parse_value()?,
            },
            Token::Gte => JqlExpr::Compare {
                field,
                op: CompareOp::Gte,
                value: self.parse_value()?,
            },
            Token::Lt => JqlExpr::Compare {
                field,
                op: CompareOp::Lt,
                value: self.parse_value()?,
            },
            Token::Lte => JqlExpr::Compare {
                field,
                op: CompareOp::Lte,
                value: self.parse_value()?,
            },
            Token::Tilde => JqlExpr::Compare {
                field,
                op: CompareOp::Contains,
                value: self.parse_value()?,
            },
            Token::NotTilde => JqlExpr::Compare {
                field,
                op: CompareOp::NotContains,
                value: self.parse_value()?,
            },
            Token::KwIn => JqlExpr::In {
                field,
                negate: false,
                values: self.parse_value_list()?,
            },
            Token::KwNot => {
                self.expect(&Token::KwIn, "`IN` after `NOT`")?;
                JqlExpr::In {
                    field,
                    negate: true,
                    values: self.parse_value_list()?,
                }
            }
            Token::KwIs => {
                let negate = self.eat(&Token::KwNot);
                self.expect(&Token::KwEmpty, "`EMPTY` after `IS`")?;
                JqlExpr::IsEmpty { field, negate }
            }
            _ => return err(format!("Expected an operator after field `{field}`")),
        };
        Ok(expr)
    }
}

// ---------------------------------------------------------------------------------------------
// Compiler: AST -> SQL fragment, validated against the entity's own metadata
// ---------------------------------------------------------------------------------------------

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
    let mut parser = Parser { tokens, pos: 0 };
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

#[cfg(test)]
mod tests {
    use super::*;
    use metap_metadata::EntityField;

    fn test_entity() -> EntityDefinition {
        EntityDefinition {
            name: "jira.issues".to_string(),
            label: "Issue".to_string(),
            table_name: "records".to_string(),
            fields: vec![
                EntityField {
                    name: "priority".to_string(),
                    label: "Priority".to_string(),
                    kind: FieldKind::Enum,
                    required: None,
                    indexed: None,
                    unique: None,
                    enum_values: Some(vec!["low".to_string(), "high".to_string()]),
                    ref_entity: None,
                    ref_display_field: None,
                    searchable: None,
                    search_mode: None,
                    sortable: Some(true),
                    storage: None,
                },
                EntityField {
                    name: "storyPoints".to_string(),
                    label: "Story Points".to_string(),
                    kind: FieldKind::Number,
                    required: None,
                    indexed: None,
                    unique: None,
                    enum_values: None,
                    ref_entity: None,
                    ref_display_field: None,
                    searchable: None,
                    search_mode: None,
                    sortable: Some(true),
                    storage: None,
                },
                EntityField {
                    name: "title".to_string(),
                    label: "Title".to_string(),
                    kind: FieldKind::String,
                    required: None,
                    indexed: None,
                    unique: None,
                    enum_values: None,
                    ref_entity: None,
                    ref_display_field: None,
                    searchable: Some(true),
                    search_mode: None,
                    sortable: None,
                    storage: None,
                },
            ],
            list_views: vec![],
            workflow: None,
        }
    }

    fn compile(jql: &str) -> JqlCompileResult {
        let entity = test_entity();
        let mut params = ParamBuilder::new();
        parse_and_compile_jql(jql, &entity, false, &mut params)
    }

    #[test]
    fn simple_equality_compiles_and_binds_one_param() {
        let entity = test_entity();
        let mut params = ParamBuilder::new();
        let (sql, order) = parse_and_compile_jql("priority = \"high\"", &entity, false, &mut params).unwrap();
        assert!(!sql.unwrap().contains("ILIKE"));
        assert!(order.is_none());
        assert_eq!(params.params.len(), 2); // jsonb key placeholder + value placeholder
    }

    #[test]
    fn and_or_not_precedence_and_parens_all_parse() {
        assert!(compile("priority = \"high\" AND storyPoints > 3").unwrap().0.is_some());
        assert!(compile("priority = \"high\" OR priority = \"low\"")
            .unwrap()
            .0
            .is_some());
        assert!(compile("NOT priority = \"high\"").unwrap().0.is_some());
        assert!(
            compile("(priority = \"high\" OR priority = \"low\") AND storyPoints >= 1")
                .unwrap()
                .0
                .is_some()
        );
    }

    #[test]
    fn in_and_is_empty_compile() {
        assert!(compile("priority IN (\"high\", \"low\")").unwrap().0.is_some());
        assert!(compile("priority NOT IN (\"high\")").unwrap().0.is_some());
        assert!(compile("storyPoints IS EMPTY").unwrap().0.is_some());
        assert!(compile("storyPoints IS NOT EMPTY").unwrap().0.is_some());
    }

    #[test]
    fn contains_operator_works_on_text_fields() {
        assert!(compile("title ~ \"kanban\"").unwrap().0.is_some());
    }

    #[test]
    fn order_by_compiles_to_field_and_direction() {
        let (_, order) = compile("priority = \"high\" ORDER BY storyPoints DESC").unwrap();
        assert_eq!(order, Some(("storyPoints".to_string(), true)));
    }

    #[test]
    fn order_by_only_with_no_filter_expression_is_allowed() {
        let (where_sql, order) = compile("ORDER BY storyPoints ASC").unwrap();
        assert!(where_sql.is_none());
        assert_eq!(order, Some(("storyPoints".to_string(), false)));
    }

    #[test]
    fn unknown_field_is_a_clean_error_not_a_panic() {
        let err = compile("nope = \"x\"").unwrap_err();
        assert!(err.0.contains("Unknown field"));
    }

    #[test]
    fn range_operator_rejected_on_non_orderable_kind() {
        let err = compile("priority > \"high\"").unwrap_err();
        assert!(err.0.contains("not supported"));
    }

    #[test]
    fn order_by_on_a_non_sortable_field_is_rejected() {
        let err = compile("title = \"x\" ORDER BY title").unwrap_err();
        assert!(err.0.contains("not sortable"));
    }

    #[test]
    fn unterminated_string_and_trailing_garbage_are_clean_errors() {
        assert!(compile("priority = \"high").is_err());
        assert!(compile("priority = \"high\" wat").is_err());
    }
}
