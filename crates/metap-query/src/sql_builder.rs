//! A minimal, hand-rolled parameter tracker filling the role Drizzle's `SQL` type plays in
//! the TS codebase (composable fragments with a shared, correctly-numbered `$N` parameter
//! sequence). `sqlx::QueryBuilder` isn't a fit here because `WHERE`/`ORDER BY` are built as
//! two separately-typed pieces (`condition-to-sql.ts`/`query-planner.ts` are two files
//! there too) that later get concatenated into one `SELECT` — this keeps that same shape
//! while sharing one placeholder counter across both.

use uuid::Uuid;

#[derive(Debug, Clone)]
pub enum BindValue {
    Text(String),
    Bool(bool),
    Uuid(Uuid),
}

#[derive(Debug, Clone, Default)]
pub struct ParamBuilder {
    pub params: Vec<BindValue>,
}

impl ParamBuilder {
    pub fn new() -> Self {
        Self::default()
    }

    /// Appends a value and returns its `$N` placeholder text.
    pub fn push(&mut self, value: BindValue) -> String {
        self.params.push(value);
        format!("${}", self.params.len())
    }
}

/// Applies a `PlannedListQuery`'s params to an `sqlx::query::Query` in order — the runtime
/// counterpart to `ParamBuilder`'s `$N` numbering. `CrudService` (Migration Order step 7)
/// is the intended caller; exposed here too since it's exactly what verifying `QueryPlanner`
/// against a real database needs (see `tests/query_planner_postgres.rs`).
pub fn apply_params<'q>(
    mut query: sqlx::query::Query<'q, sqlx::Postgres, sqlx::postgres::PgArguments>,
    params: &'q [BindValue],
) -> sqlx::query::Query<'q, sqlx::Postgres, sqlx::postgres::PgArguments> {
    for p in params {
        query = match p {
            BindValue::Text(s) => query.bind(s),
            BindValue::Bool(b) => query.bind(b),
            BindValue::Uuid(u) => query.bind(u),
        };
    }
    query
}
