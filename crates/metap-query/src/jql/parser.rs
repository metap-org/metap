//! Recursive-descent parser (`super`'s doc comment): OR lowest precedence, then AND, then NOT,
//! then a comparison atom — turns a `Vec<Token>` (`super::lexer`) into a `ParsedJql`
//! (`super::ast`).

use super::ast::{CompareOp, JqlExpr, JqlOrder, JqlValue, ParsedJql};
use super::lexer::{err, InvalidJqlError, Token};

pub(crate) struct Parser {
    tokens: Vec<Token>,
    pos: usize,
}

impl Parser {
    pub(crate) fn new(tokens: Vec<Token>) -> Self {
        Self { tokens, pos: 0 }
    }

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

    pub(crate) fn parse_query(&mut self) -> Result<ParsedJql, InvalidJqlError> {
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
