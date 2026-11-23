package router_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/kit/httpx/router"
)

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func tag(name string) router.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("X-Order", name)
			next.ServeHTTP(w, r)
		})
	}
}

func ok(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }

func TestPathValuesIncludingNamesWithADot(t *testing.T) {
	r := router.New()
	r.Get("/api/{entity}/{id}", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(req.PathValue("entity") + "|" + req.PathValue("id")))
	})
	rec := do(r, "GET", "/api/waf.zones/42")
	assert.Equal(t, 200, rec.Code)
	assert.Equal(t, "waf.zones|42", rec.Body.String())
}

func TestMiddlewareOrderGlobalThenGroupThenNested(t *testing.T) {
	r := router.New()
	r.Use(tag("global1"), tag("global2"))
	g := r.Group(tag("group"))
	g.Route("/x", func(r *router.Router) { r.Group(tag("nested")).Get("/y", ok) })
	rec := do(r, "GET", "/x/y")
	assert.Equal(t, 200, rec.Code)
	assert.Equal(t, []string{"global1", "global2", "group", "nested"}, rec.Header().Values("X-Order"))

	// A route outside the group does not see the group's middleware.
	r.Get("/plain", ok)
	assert.Equal(t, []string{"global1", "global2"}, do(r, "GET", "/plain").Header().Values("X-Order"))
}

func TestUnmatchedRequestsAnswerInTheStandardEnvelope(t *testing.T) {
	r := router.New()
	r.Use(tag("global"))
	r.Get("/only-get", ok)

	rec := do(r, "GET", "/nope")
	assert.Equal(t, 404, rec.Code)
	assert.JSONEq(t, `{"error":{"code":"not_found","message":"No such route."}}`, rec.Body.String())
	assert.Equal(t, []string{"global"}, rec.Header().Values("X-Order"), "global middleware also wraps a 404")

	rec = do(r, "POST", "/only-get")
	assert.Equal(t, 405, rec.Code)
	assert.Contains(t, rec.Header().Get("Allow"), "GET")
	assert.JSONEq(t, `{"error":{"code":"method_not_allowed","message":"Method not allowed."}}`, rec.Body.String())
}

func TestMountStripsThePrefixAndCoversTheRoot(t *testing.T) {
	sub := http.NewServeMux()
	sub.HandleFunc("GET /Users/{id}", func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte("user " + req.PathValue("id"))) })
	sub.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("root")) })
	r := router.New()
	r.Group(tag("g")).Mount("/scim/v2", sub)

	rec := do(r, "GET", "/scim/v2/Users/7")
	assert.Equal(t, "user 7", rec.Body.String())
	assert.Equal(t, []string{"g"}, rec.Header().Values("X-Order"))
	assert.Equal(t, "root", do(r, "GET", "/scim/v2/").Body.String())
	assert.Equal(t, 404, do(r, "GET", "/scim/v2/Nope").Code)
}

func TestMountUnderAPrefixedRoute(t *testing.T) {
	r := router.New()
	r.Route("/admin", func(r *router.Router) {
		r.Mount("/sub", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte(req.URL.Path)) }))
	})
	assert.Equal(t, "/a/b", do(r, "GET", "/admin/sub/a/b").Body.String())
}

func TestErrReturnsFailuresThroughOneWriter(t *testing.T) {
	var got error
	r := router.New(router.WithErrorWriter(func(w http.ResponseWriter, _ *http.Request, err error) {
		got = err
		w.WriteHeader(http.StatusTeapot)
	}))
	r.Get("/fail", r.Err(func(http.ResponseWriter, *http.Request) error { return errors.New("boom") }))
	r.Get("/fine", r.Err(func(w http.ResponseWriter, _ *http.Request) error { _, _ = w.Write([]byte("fine")); return nil }))

	assert.Equal(t, http.StatusTeapot, do(r, "GET", "/fail").Code)
	require.EqualError(t, got, "boom")
	assert.Equal(t, "fine", do(r, "GET", "/fine").Body.String())
}

func TestDefaultErrorWriterHidesTheError(t *testing.T) {
	r := router.New()
	r.Get("/fail", r.Err(func(http.ResponseWriter, *http.Request) error { return errors.New("secret detail") }))
	rec := do(r, "GET", "/fail")
	assert.Equal(t, 500, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret detail")
}

func TestRoutesListWhatIsRegisteredWithTheirLabels(t *testing.T) {
	r := router.New()
	r.Get("/health", ok)
	authed := r.Labelled("authenticated", tag("a"))
	authed.Get("/me", ok)
	authed.Labelled("admin", tag("b")).Route("/admin", func(r *router.Router) { r.Post("/users", ok) })

	assert.Equal(t, []router.Route{
		{Pattern: "GET /health", Labels: []string{}},
		{Pattern: "GET /me", Labels: []string{"authenticated"}},
		{Pattern: "POST /admin/users", Labels: []string{"authenticated", "admin"}},
	}, r.Routes())
}

func TestConflictingPatternsPanicAtRegistration(t *testing.T) {
	r := router.New()
	r.Get("/a/{x}", ok)
	assert.Panics(t, func() { r.Get("/a/{y}", ok) })
	assert.Panics(t, func() { r.Route("/bad/", func(*router.Router) {}) })
	assert.Panics(t, func() { r.Handle("GET nope", http.HandlerFunc(ok)) })
}
