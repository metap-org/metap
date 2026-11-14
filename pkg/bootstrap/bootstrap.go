// Package bootstrap declares the roles, role grants and initial accounts a freshly deployed product
// needs, and applies them to a tenant idempotently.
//
// Declaration lives in Go next to the entities it grants on (Submit from an init(), same shape as
// metadata.SubmitEntity). Applying is additive only: Apply creates what is missing and never
// modifies or deletes anything an admin has since changed, so it is safe to run on every boot or
// as a one-shot job. Wildcards in a grant ("*", "waf.*") are expanded against the registry at
// apply time into concrete per-entity, per-action policy rows; the permission engine itself has no
// wildcard support and gains none here.
package bootstrap

import (
	"fmt"
	"strings"
	"sync"
)

// AllActions in a Grant expands to every entity action (read, create, update, delete, transition).
const AllActions = "*"

// Grant gives one role a set of actions on every entity matching Entities.
type Grant struct {
	Role string
	// Entities are exact names ("waf.zones"), a prefix wildcard ("waf.*") or "*" for every entity.
	Entities []string
	// Actions are read|create|update|delete|transition, a workflow action as "transition:<name>",
	// or AllActions.
	Actions []string
}

// User is an initial account. The password is never declared here: PasswordRef names where Apply's
// PasswordSource finds it (an environment variable by default).
type User struct {
	Email       string
	Roles       []string
	PasswordRef string
}

// Bootstrap is one product's (or one service's) declaration. Fragments from several packages are
// merged with Merge/Collect.
type Bootstrap struct {
	// Roles are the role names this declaration introduces. A role is just a string in user_roles,
	// so listing one only lets Validate catch a grant or user naming a role nobody declared.
	Roles  []string
	Grants []Grant
	Users  []User
}

var (
	submissionsMu sync.Mutex
	submitted     []func() Bootstrap
)

// Submit registers a fragment for Collect. Call it from an init() next to the entity definitions.
func Submit(factory func() Bootstrap) {
	submissionsMu.Lock()
	defer submissionsMu.Unlock()
	submitted = append(submitted, factory)
}

// Collect merges every fragment submitted anywhere in this binary's import graph.
func Collect() Bootstrap {
	submissionsMu.Lock()
	factories := append([]func() Bootstrap{}, submitted...)
	submissionsMu.Unlock()

	var out Bootstrap
	for _, f := range factories {
		out = Merge(out, f())
	}
	return out
}

// Merge concatenates declarations, dropping exact duplicate roles and users (by email).
func Merge(parts ...Bootstrap) Bootstrap {
	var out Bootstrap
	roles := map[string]bool{}
	users := map[string]bool{}
	for _, p := range parts {
		for _, r := range p.Roles {
			if !roles[r] {
				roles[r] = true
				out.Roles = append(out.Roles, r)
			}
		}
		out.Grants = append(out.Grants, p.Grants...)
		for _, u := range p.Users {
			key := strings.ToLower(u.Email)
			if !users[key] {
				users[key] = true
				out.Users = append(out.Users, u)
			}
		}
	}
	return out
}

// Validate checks a declaration on its own, before anything touches a database.
func (b Bootstrap) Validate() error {
	declared := map[string]bool{}
	for _, r := range b.Roles {
		if strings.TrimSpace(r) == "" {
			return fmt.Errorf("bootstrap: empty role name")
		}
		declared[r] = true
	}
	for i, g := range b.Grants {
		if g.Role == "" {
			return fmt.Errorf("bootstrap: grant %d has no role", i)
		}
		if !declared[g.Role] {
			return fmt.Errorf("bootstrap: grant %d names undeclared role %q", i, g.Role)
		}
		if len(g.Entities) == 0 || len(g.Actions) == 0 {
			return fmt.Errorf("bootstrap: grant %d for role %q needs entities and actions", i, g.Role)
		}
		for _, a := range g.Actions {
			if !validAction(a) {
				return fmt.Errorf("bootstrap: grant %d has unknown action %q", i, a)
			}
		}
	}
	seen := map[string]bool{}
	for _, u := range b.Users {
		if !strings.Contains(u.Email, "@") {
			return fmt.Errorf("bootstrap: user %q is not an email", u.Email)
		}
		if seen[strings.ToLower(u.Email)] {
			return fmt.Errorf("bootstrap: user %q declared twice", u.Email)
		}
		seen[strings.ToLower(u.Email)] = true
		for _, r := range u.Roles {
			if !declared[r] && r != "admin" {
				return fmt.Errorf("bootstrap: user %q names undeclared role %q", u.Email, r)
			}
		}
	}
	return nil
}

var baseActions = []string{"read", "create", "update", "delete", "transition"}

func validAction(a string) bool {
	if a == AllActions {
		return true
	}
	for _, b := range baseActions {
		if a == b {
			return true
		}
	}
	return strings.HasPrefix(a, "transition:") && len(a) > len("transition:")
}

func expandActions(actions []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(a string) {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, a := range actions {
		if a == AllActions {
			for _, b := range baseActions {
				add(b)
			}
			continue
		}
		add(a)
	}
	return out
}

func entityMatches(pattern, name string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, ".*"):
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	default:
		return pattern == name
	}
}
