//! The parsed JQL AST — `super::parser` builds it, `super::codegen` compiles it to SQL.

#[derive(Debug, Clone, Copy, PartialEq)]
pub(crate) enum CompareOp {
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
pub(crate) enum JqlValue {
    Str(String),
    Bool(bool),
}

#[derive(Debug, Clone)]
pub(crate) enum JqlExpr {
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

pub(crate) struct JqlOrder {
    pub(crate) field: String,
    pub(crate) descending: bool,
}

pub(crate) struct ParsedJql {
    pub(crate) expr: Option<JqlExpr>,
    pub(crate) order_by: Option<JqlOrder>,
}
