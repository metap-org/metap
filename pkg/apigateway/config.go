package apigateway

import (
	"fmt"
	"strings"
)

// ParseUpstreams reads the UPSTREAMS spec: entries separated by ";", each
// "name=baseURL" optionally followed by "|prefix,prefix,...":
//
//	zones=http://zones-service:3000|/auth/,/me/,/admin/;scanning=http://scanning-service:3010
//
// Order matters: when two upstreams serve the same entity, the first one listed wins.
func ParseUpstreams(spec string) ([]Upstream, error) {
	var out []Upstream
	for _, entry := range strings.Split(spec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, rest, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("apigateway: upstream %q: want name=baseURL[|prefixes]", entry)
		}
		base, prefixes, _ := strings.Cut(rest, "|")
		u := Upstream{Name: strings.TrimSpace(name), BaseURL: strings.TrimSpace(base)}
		for _, p := range strings.Split(prefixes, ",") {
			if p = strings.TrimSpace(p); p != "" {
				u.Prefixes = append(u.Prefixes, p)
			}
		}
		out = append(out, u)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("apigateway: no upstreams configured")
	}
	return out, nil
}
