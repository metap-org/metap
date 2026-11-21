// Package apigateway is the generic HTTP gateway in front of a product's services (roadmap 116):
// it replaces the GraphQL gateway with a reverse proxy that knows nothing about any entity.
//
//   - Entity CRUD (/api/{entity}...) goes to the service that serves that entity. The gateway learns
//     which one from each upstream's own /metadata/entities, asked with the caller's credentials the
//     first time an entity is seen (and again when an unknown one shows up).
//   - The product-wide metadata (/metadata/entities, /metadata/entity-access) is every service's,
//     merged (metadata.go).
//   - Any other route goes to the upstream whose configured prefix matches it ("/auth/", "/me/",
//     "/admin/", ...), longest prefix first.
//   - GET /api/{entity}[/{id}]?expand=a,b resolves reference fields across services: the gateway
//     gathers the ids each field points to, fetches every target entity once (?ids=, batched), and
//     adds them to each record under "expanded". Fields of the same service work the same way.
//
// It authenticates nobody: every service verifies the caller's token itself. Credentials
// (Authorization, cookies, the CSRF header) pass through untouched.
package apigateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/httpx"
)

// Upstream is one service behind the gateway.
type Upstream struct {
	Name string
	// BaseURL is the service's HTTP root, e.g. "http://zones-service:3000".
	BaseURL string
	// Prefixes are the non-entity routes this service answers ("/auth/", "/me/", "/admin/").
	Prefixes []string
}

// Config configures a Gateway.
type Config struct {
	Upstreams []Upstream
	// Client makes the gateway's own calls (metadata discovery, expand); defaults to a 15s timeout.
	Client *http.Client
	// RediscoverAfter is the least time between two discoveries triggered by an unknown entity.
	RediscoverAfter time.Duration
	// TrustedProxies are the proxies in front of the gateway (nginx, a load balancer) whose
	// X-Forwarded-For is believed; the gateway's own calls pass the caller's address on to the
	// services so they can tell callers apart. Empty: the TCP peer is the caller.
	TrustedProxies []netip.Prefix
}

// maxIDsPerCall matches the services' bound on GET /api/{entity}?ids=.
const maxIDsPerCall = 200

// forwardedHeaders are the request headers the gateway's own calls carry from the caller's request.
var forwardedHeaders = []string{"Authorization", "Cookie", "X-CSRF-Token", "X-Request-Id", "Accept-Language"}

type upstream struct {
	Upstream
	base  *url.URL
	proxy *httputil.ReverseProxy
}

// Gateway is an http.Handler.
type Gateway struct {
	cfg       Config
	upstreams []*upstream
	byName    map[string]*upstream

	mu           sync.RWMutex
	owner        map[string]*upstream         // entity -> upstream serving it
	refs         map[string]map[string]string // entity -> reference field -> target entity
	discoveredAt time.Time
}

// New builds a Gateway; it fails only on a malformed upstream URL.
func New(cfg Config) (*Gateway, error) {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.RediscoverAfter == 0 {
		cfg.RediscoverAfter = 10 * time.Second
	}
	g := &Gateway{cfg: cfg, byName: map[string]*upstream{}, owner: map[string]*upstream{}, refs: map[string]map[string]string{}}
	for _, u := range cfg.Upstreams {
		base, err := url.Parse(strings.TrimRight(u.BaseURL, "/"))
		if err != nil || base.Scheme == "" || base.Host == "" {
			return nil, fmt.Errorf("apigateway: upstream %q: invalid base URL %q", u.Name, u.BaseURL)
		}
		up := &upstream{Upstream: u, base: base, proxy: httputil.NewSingleHostReverseProxy(base)}
		up.proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
			httpx.ServiceErrorResponse(w, http.StatusBadGateway, "upstream_unavailable", "The service is not reachable right now.", nil)
		}
		g.upstreams = append(g.upstreams, up)
		g.byName[u.Name] = up
	}
	return g, nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if entity, id, ok := entityPath(r.URL.Path); ok {
		up := g.ownerOf(r, entity)
		if up == nil {
			httpx.ServiceErrorResponse(w, http.StatusNotFound, "entity_not_found", "No service serves entity "+entity+".", nil)
			return
		}
		if expand := r.URL.Query().Get("expand"); expand != "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			g.serveExpanded(w, r, up, entity, id, splitList(expand))
			return
		}
		up.proxy.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/metadata/") && g.serveMetadata(w, r) {
		return
	}
	if up := g.prefixOwner(r.URL.Path); up != nil {
		up.proxy.ServeHTTP(w, r)
		return
	}
	httpx.ServiceErrorResponse(w, http.StatusNotFound, errcode.NotFound, "No such route.", nil)
}

// entityPath reads /api/{entity}[/{id}[/...]]; id is "" for the collection.
func entityPath(path string) (entity, id string, ok bool) {
	rest, found := strings.CutPrefix(path, "/api/")
	if !found || rest == "" {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) > 1 {
		id = parts[1]
	}
	return parts[0], id, parts[0] != ""
}

func (g *Gateway) prefixOwner(path string) *upstream {
	var best *upstream
	bestLen := 0
	for _, up := range g.upstreams {
		for _, p := range up.Prefixes {
			if strings.HasPrefix(path, p) && len(p) > bestLen {
				best, bestLen = up, len(p)
			}
		}
	}
	return best
}

func (g *Gateway) ownerOf(r *http.Request, entity string) *upstream {
	g.mu.RLock()
	up := g.owner[entity]
	stale := time.Since(g.discoveredAt) >= g.cfg.RediscoverAfter
	g.mu.RUnlock()
	if up != nil || !stale {
		return up
	}
	g.discover(r)
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.owner[entity]
}

type entityField struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	RefEntity *string `json:"refEntity"`
}

type entityDef struct {
	Name   string        `json:"name"`
	Fields []entityField `json:"fields"`
}

// discover asks every upstream which entities it serves, with the caller's credentials (an
// upstream that refuses or is down is skipped; its entities stay unknown until the next round).
func (g *Gateway) discover(r *http.Request) {
	owner := map[string]*upstream{}
	refs := map[string]map[string]string{}
	for _, up := range g.upstreams {
		var body struct {
			Data []entityDef `json:"data"`
		}
		if status, err := g.getJSON(r, up, "/metadata/entities", &body); err != nil || status != http.StatusOK {
			continue
		}
		for _, e := range body.Data {
			if _, taken := owner[e.Name]; taken {
				continue // first upstream listed wins, as in the GraphQL gateway
			}
			owner[e.Name] = up
			for _, f := range e.Fields {
				if f.RefEntity != nil && *f.RefEntity != "" {
					if refs[e.Name] == nil {
						refs[e.Name] = map[string]string{}
					}
					refs[e.Name][f.Name] = *f.RefEntity
				}
			}
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for name, up := range owner {
		g.owner[name] = up
	}
	for name, fields := range refs {
		g.refs[name] = fields
	}
	g.discoveredAt = time.Now()
}

// getJSON GETs path on up with the caller's credentials and decodes a JSON body into dst.
func (g *Gateway) getJSON(r *http.Request, up *upstream, pathAndQuery string, dst any) (int, error) {
	status, _, body, err := g.get(r.Context(), r, up, pathAndQuery)
	if err != nil {
		return 0, err
	}
	if status == http.StatusOK && dst != nil {
		if err := json.Unmarshal(body, dst); err != nil {
			return status, err
		}
	}
	return status, nil
}

func (g *Gateway) get(ctx context.Context, r *http.Request, up *upstream, pathAndQuery string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, up.base.String()+pathAndQuery, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	for _, h := range forwardedHeaders {
		if v := r.Header.Values(h); len(v) > 0 {
			req.Header[h] = v
		}
	}
	req.Header.Set("Accept", "application/json")
	// Whoever the services see is the gateway; tell them who is behind it, the way the reverse
	// proxy does for forwarded requests.
	req.Header.Set("X-Forwarded-For", httpx.ClientIP(r, g.cfg.TrustedProxies))
	res, err := g.cfg.Client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	return res.StatusCode, res.Header, body, err
}

// keepKeys trims a record's data object to the named keys.
func keepKeys(raw json.RawMessage, keys []string) json.RawMessage {
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return raw
	}
	out := make(map[string]json.RawMessage, len(keys))
	for _, k := range keys {
		if v, ok := data[k]; ok {
			out[k] = v
		}
	}
	trimmed, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return trimmed
}

// serveExpanded answers GET /api/{entity}[/{id}]?expand=... itself: the owning service's answer
// (without expand), with each record's reference fields resolved under "expanded".
func (g *Gateway) serveExpanded(w http.ResponseWriter, r *http.Request, up *upstream, entity, id string, fields []string) {
	g.mu.RLock()
	refs := g.refs[entity]
	g.mu.RUnlock()
	targets := map[string]string{} // field -> target entity
	var unknown []string
	for _, f := range fields {
		if t, ok := refs[f]; ok {
			targets[f] = t
		} else {
			unknown = append(unknown, f)
		}
	}
	if len(unknown) > 0 {
		httpx.ServiceErrorResponse(w, http.StatusBadRequest, errcode.ValidationFailed,
			"`expand` names fields that are not references of "+entity+".",
			map[string][]string{"expand": {strings.Join(unknown, ", ")}})
		return
	}

	q := r.URL.Query()
	q.Del("expand")
	// With ?fields= the owning service trims data to those fields, and the ids to expand must
	// survive that: ask for them too, and drop them again from the answer unless the caller wanted
	// them.
	asked := splitList(q.Get("fields"))
	if len(asked) > 0 {
		have := map[string]bool{}
		for _, f := range asked {
			have[f] = true
		}
		upstreamFields := append([]string(nil), asked...)
		for _, f := range fields {
			if !have[f] {
				upstreamFields = append(upstreamFields, f)
			}
		}
		q.Set("fields", strings.Join(upstreamFields, ","))
	}
	path := r.URL.Path
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	status, header, body, err := g.get(r.Context(), r, up, path)
	if err != nil {
		httpx.ServiceErrorResponse(w, http.StatusBadGateway, "upstream_unavailable", "The service is not reachable right now.", nil)
		return
	}
	if status != http.StatusOK {
		writeRaw(w, status, header, body)
		return
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope["data"] == nil {
		writeRaw(w, status, header, body)
		return
	}
	var records []map[string]json.RawMessage
	single := id != ""
	if single {
		var one map[string]json.RawMessage
		if err := json.Unmarshal(envelope["data"], &one); err != nil {
			writeRaw(w, status, header, body)
			return
		}
		records = []map[string]json.RawMessage{one}
	} else if err := json.Unmarshal(envelope["data"], &records); err != nil {
		writeRaw(w, status, header, body)
		return
	}

	// The ids each target entity is asked for, across every record and field.
	wanted := map[string]map[string]bool{}
	recordValues := make([]map[string]string, len(records))
	for i, rec := range records {
		var data map[string]json.RawMessage
		_ = json.Unmarshal(rec["data"], &data)
		recordValues[i] = map[string]string{}
		for field, target := range targets {
			var ref string
			if json.Unmarshal(data[field], &ref) != nil || ref == "" {
				continue
			}
			recordValues[i][field] = ref
			if wanted[target] == nil {
				wanted[target] = map[string]bool{}
			}
			wanted[target][ref] = true
		}
	}
	resolved, err := g.fetchByIDs(r, wanted)
	if err != nil {
		httpx.ServiceErrorResponse(w, http.StatusBadGateway, "upstream_unavailable", "A related service is not reachable right now.", nil)
		return
	}

	for i, rec := range records {
		expanded := map[string]json.RawMessage{}
		for field, target := range targets {
			ref, ok := recordValues[i][field]
			if !ok {
				expanded[field] = json.RawMessage("null")
				continue
			}
			if got, found := resolved[target][ref]; found {
				expanded[field] = got
			} else {
				expanded[field] = json.RawMessage("null") // gone, or not readable by the caller
			}
		}
		raw, _ := json.Marshal(expanded)
		rec["expanded"] = raw
		if len(asked) > 0 {
			rec["data"] = keepKeys(rec["data"], asked)
		}
	}
	var data json.RawMessage
	if single {
		data, _ = json.Marshal(records[0])
	} else {
		data, _ = json.Marshal(records)
	}
	envelope["data"] = data
	out, _ := json.Marshal(envelope)
	writeRaw(w, http.StatusOK, header, out)
}

// fetchByIDs gets every wanted record, one batched call per target entity (chunks of 200).
func (g *Gateway) fetchByIDs(r *http.Request, wanted map[string]map[string]bool) (map[string]map[string]json.RawMessage, error) {
	out := map[string]map[string]json.RawMessage{}
	for target, set := range wanted {
		up := g.ownerOf(r, target)
		out[target] = map[string]json.RawMessage{}
		if up == nil {
			continue // a target nobody serves expands to null, like an unreadable one
		}
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for start := 0; start < len(ids); start += maxIDsPerCall {
			end := min(start+maxIDsPerCall, len(ids))
			var body struct {
				Data []map[string]json.RawMessage `json:"data"`
			}
			status, err := g.getJSON(r, up, "/api/"+url.PathEscape(target)+"?ids="+url.QueryEscape(strings.Join(ids[start:end], ",")), &body)
			if err != nil {
				return nil, err
			}
			if status != http.StatusOK {
				continue // e.g. 403 on the whole entity: those references stay null
			}
			for _, rec := range body.Data {
				var id string
				if json.Unmarshal(rec["id"], &id) == nil {
					raw, _ := json.Marshal(rec)
					out[target][id] = raw
				}
			}
		}
	}
	return out, nil
}

func writeRaw(w http.ResponseWriter, status int, header http.Header, body []byte) {
	for _, h := range []string{"Content-Type", "X-Request-Id", "Cache-Control"} {
		if v := header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_, _ = io.Copy(w, bytes.NewReader(body))
}

func splitList(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
