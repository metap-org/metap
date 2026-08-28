//! Tokenizer for the JQL grammar (`super`'s doc comment) — turns a query string into a flat
//! `Vec<Token>`, validated only at the lexical level (quoting, number syntax); field names and
//! operator/type compatibility are checked later, by `super::codegen`.

#[derive(Debug)]
pub struct InvalidJqlError(pub String);

impl std::fmt::Display for InvalidJqlError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.0)
    }
}
impl std::error::Error for InvalidJqlError {}

pub(crate) fn err<T>(msg: impl Into<String>) -> Result<T, InvalidJqlError> {
    Err(InvalidJqlError(msg.into()))
}

// ---------------------------------------------------------------------------------------------
// Lexer
// ---------------------------------------------------------------------------------------------

#[derive(Debug, Clone, PartialEq)]
pub(crate) enum Token {
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

pub(crate) fn tokenize(input: &str) -> Result<Vec<Token>, InvalidJqlError> {
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
