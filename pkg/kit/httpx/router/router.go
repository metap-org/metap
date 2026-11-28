// Package router is the one HTTP router every metap service and every downstream service uses: a
// thin layer over the standard library's http.ServeMux (Go 1.22+ patterns, "GET /items/{id}",
// r.PathValue) that adds only what a service needs and ServeMux lacks — middleware per group,
// mounting a sub-handler under a prefix, errors in the standard envelope for 404/405, handlers that
// return an error, and a list of what is registered (so a test can check every route has a
// declared access rule).
//
// It is standard-library-only (pkg/kit rule), so the edge and the analytics API can use it too.
//
//	r := router.New()
//	r.Use(httpx.Recover)                      // wraps everything, including 404/405
//	r.Get("/health", health)
//	authed := r.Labelled("authenticated", requireLogin)
//	authed.Get("/items/{id}", getItem)        // r.PathValue("id")
//	admin := authed.Labelled("admin", requireAdmin)
//	admin.Post("/items", createItem)
//	r.Mount("/scim/v2", scimHandler)          // the prefix is stripped for the sub-handler
//
// Patterns follow ServeMux exactly (the more specific wins, a conflict panics at registration so it
// shows at start-up). Do not reimplement matching here.
package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/metap-org/metap/pkg/kit/httpx"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// HandlerFunc is a handler that returns its failure instead of writing it; Router.Err turns it
// into an http.HandlerFunc and writes the error once, through the router's error writer.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// ErrorWriter answers a failure a HandlerFunc returned.
type ErrorWriter func(w http.ResponseWriter, r *http.Request, err error)

// Route is one registered route: its ServeMux pattern and the labels of the groups it sits in
// (outermost first).
type Route struct {
	Pattern string
	Labels  []string
}

type shared struct {
	mux     *http.ServeMux
	global  []Middleware
	root    http.Handler
	routes  []Route
	onError ErrorWriter
}

// Router registers routes. A Router returned by Group, Labelled or Route shares the same ServeMux and
// route list with its parent; only the middleware, prefix and labels differ.
type Router struct {
	s      *shared
	prefix string
	mws    []Middleware
	labels []string
}

// Option configures New.
type Option func(*shared)

// WithErrorWriter sets how Err answers an error. The default is a 500 in the standard envelope.
func WithErrorWriter(w ErrorWriter) Option { return func(s *shared) { s.onError = w } }

// New returns an empty Router.
func New(opts ...Option) *Router {
	s := &shared{mux: http.NewServeMux(), onError: defaultErrorWriter}
	for _, o := range opts {
		o(s)
	}
	r := &Router{s: s}
	r.s.root = http.HandlerFunc(r.s.dispatch)
	return r
}

func defaultErrorWriter(w http.ResponseWriter, _ *http.Request, err error) {
	httpx.InternalErrorResponse(w, err)
}

// Use adds middleware that wraps every request the Router serves, including those that match no
// route (404/405), so it must be called before serving. Middleware added to a group with Group or
// Labelled applies only to that group's routes.
func (r *Router) Use(mw ...Middleware) {
	r.s.global = append(r.s.global, mw...)
	h := http.HandlerFunc(r.s.dispatch)
	var root http.Handler = h
	for i := len(r.s.global) - 1; i >= 0; i-- {
		root = r.s.global[i](root)
	}
	r.s.root = root
}

// ServeHTTP implements http.Handler.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	holder := &patternHolder{}
	r.s.root.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), patternKey{}, holder)))
}

type patternKey struct{}

type patternHolder struct{ pattern string }

// Pattern is the ServeMux pattern the request matched ("GET /api/{entity}/{id}"), or "" when none
// matched. Unlike http.Request.Pattern it can be read by a middleware after next.ServeHTTP returns
// even if a later middleware replaced the request (WithContext), because the pattern is stored in a
// holder every copy of the request shares; that is what a metrics label needs (a route pattern, never
// the raw path).
func Pattern(r *http.Request) string {
	if h, ok := r.Context().Value(patternKey{}).(*patternHolder); ok {
		return h.pattern
	}
	return ""
}

// dispatch routes through the mux and answers an unmatched request in the standard envelope.
func (s *shared) dispatch(w http.ResponseWriter, req *http.Request) {
	h, pattern := s.mux.Handler(req)
	if holder, ok := req.Context().Value(patternKey{}).(*patternHolder); ok {
		holder.pattern = pattern
	}
	if pattern != "" {
		// Serve through the mux itself so r.PathValue is set and its own redirects (a missing
		// trailing slash, a cleaned path) still happen.
		s.mux.ServeHTTP(w, req)
		return
	}
	probe := &probeWriter{header: http.Header{}}
	h.ServeHTTP(probe, req)
	switch probe.status {
	case http.StatusMethodNotAllowed:
		if allow := probe.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
	case http.StatusNotFound:
		httpx.WriteError(w, http.StatusNotFound, "not_found", "No such route.")
	default:
		// A redirect ServeMux chose (trailing slash, cleaned path): pass it on as it wrote it.
		for k, v := range probe.header {
			w.Header()[k] = v
		}
		w.WriteHeader(probe.status)
		_, _ = w.Write(probe.body)
	}
}

type probeWriter struct {
	header http.Header
	status int
	body   []byte
}

func (p *probeWriter) Header() http.Header { return p.header }
func (p *probeWriter) WriteHeader(code int) {
	if p.status == 0 {
		p.status = code
	}
}
func (p *probeWriter) Write(b []byte) (int, error) {
	if p.status == 0 {
		p.status = http.StatusOK
	}
	p.body = append(p.body, b...)
	return len(b), nil
}

// Group returns a Router whose routes also pass through mw (after the parent's middleware).
func (r *Router) Group(mw ...Middleware) *Router {
	return &Router{s: r.s, prefix: r.prefix, mws: append(append([]Middleware{}, r.mws...), mw...), labels: r.labels}
}

// Labelled is Group that also records label on every route registered through it, so Routes
// can say what access rule each route was put under ("authenticated", "admin", "public"...).
func (r *Router) Labelled(label string, mw ...Middleware) *Router {
	g := r.Group(mw...)
	g.labels = append(append([]string{}, r.labels...), label)
	return g
}

// Route calls fn with a Router whose routes all live under prefix ("/admin"). prefix must start
// with "/" and not end with one.
func (r *Router) Route(prefix string, fn func(*Router)) {
	checkPrefix(prefix)
	sub := r.Group()
	sub.prefix = r.prefix + prefix
	fn(sub)
}

func checkPrefix(p string) {
	if !strings.HasPrefix(p, "/") || (len(p) > 1 && strings.HasSuffix(p, "/")) {
		panic(fmt.Sprintf("router: prefix %q must start with / and not end with one", p))
	}
}

// Handle registers h for a ServeMux pattern ("GET /items/{id}", "/healthz", "POST /a/{rest...}").
// The router's prefix goes after the method; the group's middleware wraps h.
func (r *Router) Handle(pattern string, h http.Handler) {
	full := r.fullPattern(pattern)
	for i := len(r.mws) - 1; i >= 0; i-- {
		h = r.mws[i](h)
	}
	r.s.mux.Handle(full, h)
	r.s.routes = append(r.s.routes, Route{Pattern: full, Labels: append([]string{}, r.labels...)})
}

// HandleFunc is Handle for a function. Its signature is ServeMux's own, so a Router can stand in for
// a *http.ServeMux where an adapter (huma's humago) asks for one.
func (r *Router) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) {
	r.Handle(pattern, http.HandlerFunc(f))
}

func (r *Router) fullPattern(pattern string) string {
	method, path, hasMethod := strings.Cut(pattern, " ")
	if !hasMethod {
		method, path = "", pattern
	} else {
		method += " "
	}
	if !strings.HasPrefix(path, "/") {
		panic(fmt.Sprintf("router: pattern %q has no path starting with /", pattern))
	}
	return method + r.prefix + path
}

// Get, Post, Put, Patch and Delete register "METHOD path".
func (r *Router) Get(path string, f http.HandlerFunc)    { r.HandleFunc("GET "+path, f) }
func (r *Router) Post(path string, f http.HandlerFunc)   { r.HandleFunc("POST "+path, f) }
func (r *Router) Put(path string, f http.HandlerFunc)    { r.HandleFunc("PUT "+path, f) }
func (r *Router) Patch(path string, f http.HandlerFunc)  { r.HandleFunc("PATCH "+path, f) }
func (r *Router) Delete(path string, f http.HandlerFunc) { r.HandleFunc("DELETE "+path, f) }

// Mount serves h under prefix for every method, with the prefix stripped from the path h sees
// (prefix "/scim/v2": a request for /scim/v2/Users reaches h as /Users).
func (r *Router) Mount(prefix string, h http.Handler) {
	checkPrefix(prefix)
	strip := http.StripPrefix(r.prefix+prefix, h)
	r.Handle(prefix, strip)
	r.Handle(prefix+"/{rest...}", strip)
}

// Err turns a handler that returns its failure into an http.HandlerFunc; the failure is written once,
// by the router's error writer (WithErrorWriter). A handler that wrote its own response returns nil.
func (r *Router) Err(f HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := f(w, req); err != nil && !errors.Is(err, http.ErrAbortHandler) {
			r.s.onError(w, req, err)
		}
	}
}

// Routes lists what is registered, in registration order.
func (r *Router) Routes() []Route { return append([]Route{}, r.s.routes...) }
