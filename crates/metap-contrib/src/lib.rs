//! Shared plumbing and reference implementations for how *this repo* builds services — a common
//! base for `metap`'s own binaries (`graphql-gateway`, `cron-scheduler`, `metap-jwks`, ...) and
//! for `../metap-lowcode`'s crates, which depend on this the same way they already depend on
//! `metap-http`/`metap-infra` (path dependency into `../../../metap/crates/metap-contrib`).
//!
//! Not a grab-bag: every module here exists because `docs/features/08-metap-contrib-common-crate.md`
//! found *actual* duplicated boilerplate across >= 2 call sites (verified by reading the code, not
//! guessed), and this is meant to grow the same way — a new helper lands here only once a second
//! real caller would otherwise copy-paste the first one's logic. No `metap-*` crate business logic
//! (entities, tenants, workflow) belongs here; this is pure cross-cutting plumbing.

pub mod bearer;
pub mod env;
pub mod http_client;
