package apigateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/httpx"
)

// The product-wide metadata a UI reads once (roadmap 116): every service answers for its own
// entities, so the gateway merges.
//
//   - GET /metadata/entities: every upstream's list, one entry per entity (first upstream wins).
//   - GET /metadata/entity-access: every upstream's map, merged.
//   - GET /metadata/entities/{entity}: the service that serves the entity.
//   - anything else under /metadata/ (actions, openapi): a configured prefix, else the first upstream.
func (g *Gateway) serveMetadata(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch path := r.URL.Path; {
	case path == "/metadata/entities":
		g.mergeLists(w, r, path)
	case path == "/metadata/entity-access":
		g.mergeMaps(w, r, path)
	case strings.HasPrefix(path, "/metadata/entities/"):
		entity := strings.TrimPrefix(path, "/metadata/entities/")
		up := g.ownerOf(r, entity)
		if up == nil {
			httpx.ServiceErrorResponse(w, http.StatusNotFound, errcode.EntityNotFound, "Entity not found.", nil)
			return true
		}
		up.proxy.ServeHTTP(w, r)
	default:
		if g.prefixOwner(path) != nil || len(g.upstreams) == 0 {
			return false
		}
		g.upstreams[0].proxy.ServeHTTP(w, r)
	}
	return true
}

// fanOut GETs path on every upstream with the caller's credentials. The first non-200 answer is
// returned as-is when no upstream answered 200 (a 401 stays a 401).
func (g *Gateway) fanOut(w http.ResponseWriter, r *http.Request, path string) ([][]byte, bool) {
	var bodies [][]byte
	var failStatus int
	var failHeader http.Header
	var failBody []byte
	for _, up := range g.upstreams {
		status, header, body, err := g.get(r.Context(), r, up, path)
		if err != nil {
			continue
		}
		if status != http.StatusOK {
			if failStatus == 0 {
				failStatus, failHeader, failBody = status, header, body
			}
			continue
		}
		bodies = append(bodies, body)
	}
	if len(bodies) == 0 {
		if failStatus == 0 {
			httpx.ServiceErrorResponse(w, http.StatusBadGateway, "upstream_unavailable", "No service answered.", nil)
		} else {
			writeRaw(w, failStatus, failHeader, failBody)
		}
		return nil, false
	}
	return bodies, true
}

func (g *Gateway) mergeLists(w http.ResponseWriter, r *http.Request, path string) {
	bodies, ok := g.fanOut(w, r, path)
	if !ok {
		return
	}
	seen := map[string]bool{}
	merged := []json.RawMessage{}
	for _, body := range bodies {
		var page struct {
			Data []json.RawMessage `json:"data"`
		}
		if json.Unmarshal(body, &page) != nil {
			continue
		}
		for _, item := range page.Data {
			var named struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(item, &named)
			if named.Name != "" && seen[named.Name] {
				continue
			}
			seen[named.Name] = true
			merged = append(merged, item)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": merged})
}

func (g *Gateway) mergeMaps(w http.ResponseWriter, r *http.Request, path string) {
	bodies, ok := g.fanOut(w, r, path)
	if !ok {
		return
	}
	merged := map[string]json.RawMessage{}
	for _, body := range bodies {
		var page struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if json.Unmarshal(body, &page) != nil {
			continue
		}
		for k, v := range page.Data {
			if _, taken := merged[k]; !taken {
				merged[k] = v
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": merged})
}
