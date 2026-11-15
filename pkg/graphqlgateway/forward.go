package graphqlgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/graphql-go/graphql"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/permission"
)

// JSONScalar is the "Json" scalar every dynamic field uses for free-form values.
var JSONScalar = jsonScalar

// RequestContextFrom returns the caller's RequestContext inside a resolver (never nil).
func RequestContextFrom(ctx context.Context) *permission.RequestContext {
	return requestContextFrom(ctx)
}

func httpClientForForwarding() *http.Client {
	return &http.Client{Timeout: forwardTimeout}
}

// ForwardPost forwards the caller's own bearer token to a POST REST endpoint of an upstream and
// returns its JSON body as a GraphQL value; a non-2xx answer becomes an error carrying the
// upstream's message. It is how a product's custom mutation reaches the upstream that implements
// the real logic. A missing token means the gateway's own auth gate somehow let an unauthenticated
// request through — not expected, but a clear error beats a confusing downstream 401.
func ForwardPost(ctx context.Context, url string, body interface{}) (interface{}, error) {
	rc := requestContextFrom(ctx)
	if rc.ForwardedBearerToken == "" {
		return nil, fmt.Errorf("no caller token to forward — this should be unreachable")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set(standard.HeaderContentType, standard.MediaTypeJSON)
	req.Header.Set(standard.HeaderAuthorization, standard.BearerPrefix+rc.ForwardedBearerToken)
	resp, err := httpClientForForwarding().Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, err := readUpstream(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response from %s: %w", url, err)
	}
	// Bespoke schema-less forwarding: the upstream's JSON has no single concrete type by design, so
	// it is only ever read back through type-switched lookups, never trusted.
	var parsed interface{} // nosemgrep: go.lang.security.deserialization.unsafe-deserialization-interface.go-unsafe-deserialization-interface
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parsing response from %s: %w", url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := "request failed"
		if obj, ok := parsed.(map[string]interface{}); ok {
			if errObj, ok := obj["error"].(map[string]interface{}); ok {
				if m, ok := errObj["message"].(string); ok {
					message = m
				}
			}
		}
		return nil, fmt.Errorf("%s", message)
	}
	return parsed, nil
}

// IDArg reads a required non-empty string/ID argument inside a resolver.
func IDArg(p graphql.ResolveParams, name string) (string, error) {
	s, ok := p.Args[name].(string)
	if !ok || s == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return s, nil
}

// WithRequestContext attaches the caller's RequestContext (and a fresh per-request record cache)
// to ctx, as the HTTP handler does before executing a query. Exported for tests of a product's
// own fields, which run queries with graphql.Do directly.
func WithRequestContext(ctx context.Context, rc *permission.RequestContext) context.Context {
	return withRequestContext(ctx, rc)
}

// InvokeAction runs an entity action on an upstream's ActionService and returns its JSON object
// as a GraphQL value, with a *crud.ServiceError reported through the usual extensions envelope.
// Product-specific mutations that keep an older name or argument shape call this instead of
// duplicating the gRPC plumbing; the generic "<action><TypeName>" fields use it too.
func InvokeAction(ctx context.Context, inv crud.ActionInvoker, call crud.ActionCall) (interface{}, error) {
	out, err := inv.Invoke(ctx, call)
	if err != nil {
		return nil, gqlError(err)
	}
	return jsonObjectToMap(out)
}

// ParseUUID parses a record id argument.
func ParseUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%q is not a valid id", s)
	}
	return id, nil
}

// forwardTimeout bounds one forwarded REST call to an upstream (platform fields, ForwardPost).
const forwardTimeout = 15 * time.Second
