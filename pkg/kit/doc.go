// Package kit is the root of metap's general-purpose Go utilities: small, standard-library-only
// packages (env, shutdown, httpx) any Go service can import — including the parts of a
// downstream product that are deliberately not built on metap's metadata/CRUD platform (e.g.
// ../waap's control-plane and edge-plane).
//
// Rule, enforced by depguard (.golangci.yml): packages under pkg/kit import only the standard
// library and other pkg/kit packages — never pkg/crud, pkg/metadata, pgx, prometheus, etc. —
// so importing one never pulls the management-plane platform into a binary.
package kit
