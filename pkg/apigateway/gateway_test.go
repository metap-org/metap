package apigateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two services as in WAAP: scan jobs (scanning) reference zones (zones, also the issuer of /me/).
func fakeServices(t *testing.T) (scanning, zones *httptest.Server, zoneCalls *atomic.Int32) {
	t.Helper()
	zoneCalls = &atomic.Int32{}
	requireAuth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"no"}}`))
			return false
		}
		return true
	}
	scanning = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/metadata/entities":
			_, _ = w.Write([]byte(`{"data":[{"name":"waf.scan_jobs","fields":[{"name":"zoneId","kind":"reference","refEntity":"waf.zones"},{"name":"name","kind":"string"}]}]}`))
		case r.URL.Path == "/api/waf.scan_jobs":
			assert.Empty(t, r.URL.Query().Get("expand"), "expand is the gateway's, not forwarded")
			_, _ = w.Write([]byte(`{"data":[
				{"id":"j1","entity":"waf.scan_jobs","data":{"name":"a","zoneId":"z1"}},
				{"id":"j2","entity":"waf.scan_jobs","data":{"name":"b","zoneId":"z2"}},
				{"id":"j3","entity":"waf.scan_jobs","data":{"name":"c","zoneId":"z1"}},
				{"id":"j4","entity":"waf.scan_jobs","data":{"name":"d","zoneId":"gone"}},
				{"id":"j5","entity":"waf.scan_jobs","data":{"name":"e"}}],"page":{"limit":30}}`))
		case r.URL.Path == "/api/waf.scan_jobs/j1":
			_, _ = w.Write([]byte(`{"data":{"id":"j1","entity":"waf.scan_jobs","data":{"zoneId":"z2"},"capabilities":{"canUpdate":true}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	zones = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/metadata/entities":
			_, _ = w.Write([]byte(`{"data":[{"name":"waf.zones","fields":[{"name":"hostname","kind":"string"}]}]}`))
		case r.URL.Path == "/api/waf.zones":
			zoneCalls.Add(1)
			ids := strings.Split(r.URL.Query().Get("ids"), ",")
			var out []map[string]any
			for _, id := range ids {
				if id == "z1" || id == "z2" {
					out = append(out, map[string]any{"id": id, "entity": "waf.zones", "data": map[string]any{"hostname": id + ".example.com"}})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
		case r.URL.Path == "/me/sessions":
			_, _ = w.Write([]byte(`{"sessions":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(func() { scanning.Close(); zones.Close() })
	return scanning, zones, zoneCalls
}

func gatewayFor(t *testing.T, scanning, zones *httptest.Server) *httptest.Server {
	t.Helper()
	g, err := New(Config{Upstreams: []Upstream{
		{Name: "zones", BaseURL: zones.URL, Prefixes: []string{"/me/", "/auth/"}},
		{Name: "scanning", BaseURL: scanning.URL},
	}})
	require.NoError(t, err)
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer good")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body
}

func TestRoutesEntitiesAndPrefixesToTheirService(t *testing.T) {
	scanning, zones, _ := fakeServices(t)
	gw := gatewayFor(t, scanning, zones)

	status, body := get(t, gw.URL+"/api/waf.scan_jobs")
	require.Equal(t, 200, status)
	assert.Len(t, body["data"], 5)

	status, body = get(t, gw.URL+"/me/sessions")
	require.Equal(t, 200, status)
	assert.Contains(t, body, "sessions")

	status, body = get(t, gw.URL+"/api/shop.orders")
	assert.Equal(t, 404, status)
	assert.Equal(t, "entity_not_found", body["error"].(map[string]any)["code"])

	// Credentials pass through: without them the service itself refuses.
	res, err := http.Get(gw.URL + "/me/sessions")
	require.NoError(t, err)
	assert.Equal(t, 401, res.StatusCode)
}

func TestExpandAcrossServicesIsBatched(t *testing.T) {
	scanning, zones, zoneCalls := fakeServices(t)
	gw := gatewayFor(t, scanning, zones)

	status, body := get(t, gw.URL+"/api/waf.scan_jobs?expand=zoneId")
	require.Equal(t, 200, status)
	records := body["data"].([]any)
	host := func(i int) any {
		exp := records[i].(map[string]any)["expanded"].(map[string]any)["zoneId"]
		if exp == nil {
			return nil
		}
		return exp.(map[string]any)["data"].(map[string]any)["hostname"]
	}
	assert.Equal(t, "z1.example.com", host(0))
	assert.Equal(t, "z2.example.com", host(1))
	assert.Equal(t, "z1.example.com", host(2))
	assert.Nil(t, host(3), "a reference to a record that is gone (or unreadable) expands to null")
	assert.Nil(t, host(4), "no reference, null")
	assert.EqualValues(t, 1, zoneCalls.Load(), "every zone in one call, not one per record")
	assert.Equal(t, "a", records[0].(map[string]any)["data"].(map[string]any)["name"], "the record itself is untouched")

	status, body = get(t, gw.URL+"/api/waf.scan_jobs/j1?expand=zoneId")
	require.Equal(t, 200, status)
	one := body["data"].(map[string]any)
	assert.Equal(t, "z2.example.com", one["expanded"].(map[string]any)["zoneId"].(map[string]any)["data"].(map[string]any)["hostname"])
	assert.Contains(t, one, "capabilities", "the rest of the get response is kept")
}

func TestExpandRefusesAFieldThatIsNotAReference(t *testing.T) {
	scanning, zones, _ := fakeServices(t)
	gw := gatewayFor(t, scanning, zones)
	status, body := get(t, gw.URL+"/api/waf.scan_jobs?expand=name")
	require.Equal(t, 400, status)
	errBody := body["error"].(map[string]any)
	assert.Equal(t, "validation_failed", errBody["code"])
	assert.Contains(t, errBody["fieldErrors"], "expand")
}

func TestNewRejectsAMalformedUpstream(t *testing.T) {
	_, err := New(Config{Upstreams: []Upstream{{Name: "x", BaseURL: "not a url"}}})
	assert.Error(t, err)
}

func TestMetadataIsMergedAcrossServices(t *testing.T) {
	scanning, zones, _ := fakeServices(t)
	gw := gatewayFor(t, scanning, zones)

	status, body := get(t, gw.URL+"/metadata/entities")
	require.Equal(t, http.StatusOK, status)
	names := []string{}
	for _, e := range body["data"].([]any) {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	assert.ElementsMatch(t, []string{"waf.zones", "waf.scan_jobs"}, names)

	// Without credentials every service refuses: the refusal comes back as-is.
	res, err := http.Get(gw.URL + "/metadata/entities")
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, res.StatusCode)
}
