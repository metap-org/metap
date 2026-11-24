// Package scope says whose data a read of a store outside metap may return (roadmap 121).
//
// A store outside metap (security events in ClickHouse, documents in Elasticsearch) has no entity
// and no policy engine. What it does need is the one rule metap's records already follow: a read is
// always bounded by the caller's tenant, and narrower still when the caller holds a role on only
// some zones or accounts. A Scope carries that bound, and an adapter that reads takes one instead
// of a bare tenant id, so there is no read that forgot to bound itself.
//
// Standard library only, so the edge and the analytics API (which may import only pkg/kit) use it.
package scope

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Scope is the tenant a read is for, and for each dimension it is limited on, the values it may
// see. A dimension with no limit is unrestricted; a limit only ever narrows. The zero Scope is
// invalid: it has no tenant, and every adapter refuses it.
type Scope struct {
	tenant string
	limits map[string][]string
}

// ForTenant is the scope of one tenant's data, limited on nothing yet.
func ForTenant(tenant string) Scope { return Scope{tenant: tenant} }

// Limit narrows the scope on a dimension to the given values (a zone, an account). Limiting a
// dimension that is already limited keeps only the values both allow, so a scope can be narrowed
// further but never widened. Limit with no values leaves the dimension with nothing to see. It
// returns a new Scope; s is unchanged.
func (s Scope) Limit(dimension string, values ...string) Scope {
	out := Scope{tenant: s.tenant, limits: make(map[string][]string, len(s.limits)+1)}
	for d, v := range s.limits {
		out.limits[d] = v
	}
	allowed := dedupe(values)
	if existing, ok := s.limits[dimension]; ok {
		allowed = intersect(existing, allowed)
	}
	out.limits[dimension] = allowed
	return out
}

// Tenant is the tenant the scope is for.
func (s Scope) Tenant() string { return s.tenant }

// ErrInvalid is returned for a scope that cannot bound a read.
var ErrInvalid = errors.New("scope: a read needs a scope with a tenant")

// Valid reports whether the scope can bound a read: it names a tenant.
func (s Scope) Valid() error {
	if strings.TrimSpace(s.tenant) == "" {
		return ErrInvalid
	}
	return nil
}

// Allows reports whether the scope lets value of dimension be seen: any value on a dimension it
// does not limit, and only the listed ones on a dimension it does.
func (s Scope) Allows(dimension, value string) bool {
	allowed, limited := s.limits[dimension]
	return !limited || slices.Contains(allowed, value)
}

// Limits is a copy of the limited dimensions and the values each allows.
func (s Scope) Limits() map[string][]string {
	out := make(map[string][]string, len(s.limits))
	for d, v := range s.limits {
		out[d] = slices.Clone(v)
	}
	return out
}

// Clause is one condition a read must carry: Column holds one of Values.
type Clause struct {
	Column string
	Values []string
}

// SQL renders the clause as a parameterised condition: "col = ?" for one value, "col IN (?, ?)" for
// several. placeholder is how one parameter is written for the column's type ("?" or
// "toUUID(?)"); the values are returned as arguments and are never put in the statement text.
func (c Clause) SQL(placeholder string) (string, []any) {
	args := make([]any, len(c.Values))
	for i, v := range c.Values {
		args[i] = v
	}
	if len(c.Values) == 1 {
		return c.Column + " = " + placeholder, args
	}
	marks := make([]string, len(c.Values))
	for i := range marks {
		marks[i] = placeholder
	}
	return c.Column + " IN (" + strings.Join(marks, ", ") + ")", args
}

// Plan is a scope compiled for one store: the conditions every read of it must carry.
type Plan struct {
	// Clauses are the tenant first, then each limited dimension in name order.
	Clauses []Clause
	// None is true when a limited dimension has nothing left to see: the read matches nothing, and
	// an adapter answers empty without querying.
	None bool
}

// Plan compiles the scope for a store. tenantColumn is the store's tenant column; columns maps each
// dimension the store can filter on to its column. A limited dimension the store does not list is an
// error: a limit that is silently skipped would be a hole, so it is refused.
func (s Scope) Plan(tenantColumn string, columns map[string]string) (Plan, error) {
	if err := s.Valid(); err != nil {
		return Plan{}, err
	}
	plan := Plan{Clauses: []Clause{{Column: tenantColumn, Values: []string{s.tenant}}}}
	dims := make([]string, 0, len(s.limits))
	for d := range s.limits {
		dims = append(dims, d)
	}
	slices.Sort(dims)
	for _, d := range dims {
		column, ok := columns[d]
		if !ok {
			return Plan{}, fmt.Errorf("scope: this store cannot filter on %q, so a scope limited on it cannot be applied", d)
		}
		values := s.limits[d]
		if len(values) == 0 {
			plan.None = true
			continue
		}
		plan.Clauses = append(plan.Clauses, Clause{Column: column, Values: slices.Clone(values)})
	}
	return plan, nil
}

func dedupe(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func intersect(a, b []string) []string {
	out := make([]string, 0, len(a))
	for _, v := range a {
		if slices.Contains(b, v) {
			out = append(out, v)
		}
	}
	return out
}
