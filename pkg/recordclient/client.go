// Package recordclient is a crud.RecordBackend and crud.ActionInvoker over another service's REST
// API (roadmap 116; replaced the gRPC backend client): /api/{entity}*, the batch read ?ids= and the
// declared-action routes. Each call authenticates as the caller (rc.ForwardedBearerToken) when
// there is one, else as the service account; a failure comes back as the *crud.ServiceError the
// other service answered with (status, code, message, field errors, details).
package recordclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
)

// Client talks to one service.
type Client struct {
	base         string
	http         *http.Client
	serviceToken *httpx.ServiceTokenSource
}

var (
	_ crud.RecordBackend = (*Client)(nil)
	_ crud.ActionInvoker = (*Client)(nil)
)

// New is a Client for the service at baseURL ("http://scanning-service:3010"). serviceToken signs
// calls made with no caller to forward; nil means such calls go unauthenticated.
func New(baseURL string, serviceToken *httpx.ServiceTokenSource) *Client {
	if serviceToken == nil {
		serviceToken = httpx.NewStaticServiceTokenSource("")
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 30 * time.Second}, serviceToken: serviceToken}
}

func (c *Client) token(rc *permission.RequestContext) string {
	forwarded := ""
	tenant := ""
	if rc != nil {
		forwarded, tenant = rc.ForwardedBearerToken, rc.TenantID
	}
	if forwarded == "" {
		slog.Debug("no forwarded bearer token, falling back to service account token", standard.LogTenantID, tenant)
	}
	return httpx.CallerToken(forwarded, c.serviceToken)
}

type errorEnvelope struct {
	Error struct {
		Code        string              `json:"code"`
		Message     string              `json:"message"`
		FieldErrors map[string][]string `json:"fieldErrors"`
		Details     map[string]any      `json:"details"`
	} `json:"error"`
}

// do sends one request and decodes {"data": ...} into out (nil to ignore the body).
func (c *Client) do(ctx context.Context, rc *permission.RequestContext, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set(standard.HeaderContentType, standard.MediaTypeJSON)
	}
	req.Header.Set("Accept", standard.MediaTypeJSON)
	if token := c.token(rc); token != "" {
		req.Header.Set(standard.HeaderAuthorization, standard.BearerPrefix+token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return crud.NewServiceErrorMsg(http.StatusBadGateway, "upstream_unavailable", fmt.Sprintf("%s %s: %v", method, path, err))
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var env errorEnvelope
		if json.Unmarshal(raw, &env) == nil && env.Error.Code != "" {
			se := crud.NewServiceErrorMsg(uint16(res.StatusCode), env.Error.Code, env.Error.Message)
			se.FieldErrors, se.Details = env.Error.FieldErrors, env.Error.Details
			return se
		}
		return crud.NewServiceErrorMsg(uint16(res.StatusCode), "upstream_error", strings.TrimSpace(string(raw)))
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func entityPath(entity string) string { return "/api/" + url.PathEscape(entity) }

// ListQuery is ListInput as the REST list route reads it.
func ListQuery(input query.ListInput) url.Values {
	q := url.Values{}
	if input.Limit > 0 {
		q.Set("limit", strconv.FormatInt(input.Limit, 10))
	}
	for _, f := range input.Filters {
		q.Add(f[0], f[1])
	}
	set := func(k string, v *string) {
		if v != nil {
			q.Set(k, *v)
		}
	}
	set("sort", input.Sort)
	set("cursor", input.Cursor)
	set("listView", input.ListView)
	set("jql", input.JQL)
	set("search", input.Search)
	if input.Offset > 0 {
		q.Set("offset", strconv.FormatInt(input.Offset, 10))
	}
	if input.WithTotal {
		q.Set("withTotal", "true")
	}
	return q
}

func (c *Client) List(ctx context.Context, entity string, input query.ListInput, rc *permission.RequestContext) (crud.ListResult, error) {
	var out struct {
		Data         []crud.RecordDto        `json:"data"`
		Page         crud.PageInfo           `json:"page"`
		Capabilities crud.EntityCapabilities `json:"capabilities"`
	}
	path := entityPath(entity)
	if enc := ListQuery(input).Encode(); enc != "" {
		path += "?" + enc
	}
	if err := c.do(ctx, rc, http.MethodGet, path, nil, &out); err != nil {
		return crud.ListResult{}, err
	}
	return crud.ListResult{Data: out.Data, Page: out.Page, Capabilities: out.Capabilities}, nil
}

// getRecord is the record plus "capabilities" merged at its top level, as GET /api/{e}/{id} sends it.
type getRecord struct {
	crud.RecordDto
	Capabilities crud.RecordCapabilities `json:"capabilities"`
}

func (c *Client) Get(ctx context.Context, entity string, id uuid.UUID, rc *permission.RequestContext) (crud.GetResult, error) {
	var out struct {
		Data getRecord `json:"data"`
	}
	if err := c.do(ctx, rc, http.MethodGet, entityPath(entity)+"/"+id.String(), nil, &out); err != nil {
		return crud.GetResult{}, err
	}
	return crud.GetResult{Record: out.Data.RecordDto, Capabilities: out.Data.Capabilities}, nil
}

func (c *Client) GetMany(ctx context.Context, entity string, ids []uuid.UUID, rc *permission.RequestContext) ([]crud.GetManyResult, error) {
	var results []crud.GetManyResult
	for start := 0; start < len(ids); start += 200 {
		end := min(start+200, len(ids))
		parts := make([]string, 0, end-start)
		for _, id := range ids[start:end] {
			parts = append(parts, id.String())
		}
		var out struct {
			Data []crud.RecordDto `json:"data"`
		}
		if err := c.do(ctx, rc, http.MethodGet, entityPath(entity)+"?ids="+url.QueryEscape(strings.Join(parts, ",")), nil, &out); err != nil {
			return nil, err
		}
		for _, r := range out.Data {
			results = append(results, crud.GetManyResult{ID: r.ID, Record: r})
		}
	}
	return results, nil
}

type recordOut struct {
	Data crud.RecordDto `json:"data"`
}

func (c *Client) Create(ctx context.Context, entity string, data crud.JsonObject, rc *permission.RequestContext, reason *string) (crud.RecordDto, error) {
	var out recordOut
	err := c.do(ctx, rc, http.MethodPost, entityPath(entity), map[string]any{"data": data, "reason": reason}, &out)
	return out.Data, err
}

func (c *Client) Update(ctx context.Context, entity string, id uuid.UUID, expectedVersion int32, data crud.JsonObject, rc *permission.RequestContext, reason *string) (crud.RecordDto, error) {
	var out recordOut
	err := c.do(ctx, rc, http.MethodPatch, entityPath(entity)+"/"+id.String(),
		map[string]any{"version": expectedVersion, "data": data, "reason": reason}, &out)
	return out.Data, err
}

func (c *Client) Transition(ctx context.Context, entity string, id uuid.UUID, action string, expectedVersion int32, data crud.JsonObject, rc *permission.RequestContext, reason *string) (crud.RecordDto, error) {
	var out recordOut
	err := c.do(ctx, rc, http.MethodPost, entityPath(entity)+"/"+id.String()+"/transitions/"+url.PathEscape(action),
		map[string]any{"version": expectedVersion, "data": data, "reason": reason}, &out)
	return out.Data, err
}

func (c *Client) Delete(ctx context.Context, entity string, id uuid.UUID, expectedVersion int32, rc *permission.RequestContext, reason *string) (crud.RecordDto, error) {
	var out recordOut
	err := c.do(ctx, rc, http.MethodDelete, entityPath(entity)+"/"+id.String(),
		map[string]any{"version": expectedVersion, "reason": reason}, &out)
	return out.Data, err
}

func (c *Client) Aggregate(ctx context.Context, entity string, spec query.AggregateSpec, rc *permission.RequestContext) ([]json.RawMessage, error) {
	var out struct {
		Data []json.RawMessage `json:"data"`
	}
	err := c.do(ctx, rc, http.MethodPost, entityPath(entity)+"/aggregate", spec, &out)
	return out.Data, err
}

// Invoke runs a declared action on the other service.
func (c *Client) Invoke(ctx context.Context, call crud.ActionCall) (crud.JsonObject, error) {
	path := entityPath(call.Entity) + "/actions/" + url.PathEscape(call.Name)
	if call.ID != uuid.Nil {
		path = entityPath(call.Entity) + "/" + call.ID.String() + "/actions/" + url.PathEscape(call.Name)
	}
	var out struct {
		Data crud.JsonObject `json:"data"`
	}
	err := c.do(ctx, call.RC, http.MethodPost, path, map[string]any{"input": call.Input}, &out)
	return out.Data, err
}
