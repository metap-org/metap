package httpx

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsesAWellFormedTraceparent(t *testing.T) {
	h := http.Header{}
	h.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx := TraceContextFromHeaders(h)
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", ctx.TraceID, "got trace id %q", ctx.TraceID)
	require.Equal(t, "00f067aa0ba902b7", ctx.ParentSpanID, "got parent span id %q", ctx.ParentSpanID)
	require.True(t, ctx.Sampled, "expected sampled")
	require.Equal(t, 16, len(ctx.SpanID), "got span id length %d", len(ctx.SpanID))
}

func TestStartsANewRootTraceWhenHeaderIsAbsent(t *testing.T) {
	ctx := TraceContextFromHeaders(http.Header{})
	require.Equal(t, 32, len(ctx.TraceID), "got trace id length %d", len(ctx.TraceID))
	require.Equal(t, "", ctx.ParentSpanID, "expected no parent span id")
}

func TestRejectsMalformedHeadersAndStartsANewTraceInstead(t *testing.T) {
	h := http.Header{}
	h.Set("traceparent", "not-a-valid-traceparent")
	ctx := TraceContextFromHeaders(h)
	require.Equal(t, "", ctx.ParentSpanID, "expected no parent span id")
}

func TestRejectsAllZeroTraceID(t *testing.T) {
	h := http.Header{}
	h.Set("traceparent", "00-00000000000000000000000000000000-00f067aa0ba902b7-01")
	ctx := TraceContextFromHeaders(h)
	require.Equal(t, "", ctx.ParentSpanID, "expected no parent span id")
}

func TestToTraceparentHeaderRoundTrips(t *testing.T) {
	ctx := TraceContext{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7", Sampled: true}
	header := ctx.ToTraceparentHeader()
	traceID, parentID, sampled, ok := parseTraceparent(header)
	require.True(t, ok, "expected to parse")
	require.Equal(t, ctx.TraceID, traceID, "got (%q, %q, %v)", traceID, parentID, sampled)
	require.Equal(t, ctx.SpanID, parentID, "got (%q, %q, %v)", traceID, parentID, sampled)
	require.True(t, sampled, "got (%q, %q, %v)", traceID, parentID, sampled)
}

func TestCurrentIsNoneOutsideAScope(t *testing.T) {
	_, ok := CurrentTrace(context.Background())
	require.False(t, ok, "expected not ok")
}

func TestCurrentIsSomeInsideAScope(t *testing.T) {
	ctx := TraceContextFromHeaders(http.Header{})
	wrapped := WithTraceContext(context.Background(), ctx)
	got, ok := CurrentTrace(wrapped)
	require.True(t, ok, "got (%v, %v)", got, ok)
	require.Equal(t, ctx.TraceID, got.TraceID, "got (%v, %v)", got, ok)
}
