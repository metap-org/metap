package runtime

// InsertOpenAPIPath inserts a hand-written OpenAPI path fragment into paths — ported from
// metap-runtime's openapi::insert for parity with callers that want the same call shape. In Go a
// plain map assignment (paths[path] = item) does the same thing; this exists only so a future
// OpenAPI-building package here doesn't need to re-decide that.
func InsertOpenAPIPath(paths map[string]any, path string, item any) {
	paths[path] = item
}
