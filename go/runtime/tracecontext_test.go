package runtime

import (
	"context"
	"net/http"
	"testing"
)

func TestParsesAWellFormedTraceparent(t *testing.T) {
	h := http.Header{}
	h.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx := TraceContextFromHeaders(h)
	if ctx.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("got trace id %q", ctx.TraceID)
	}
	if ctx.ParentSpanID != "00f067aa0ba902b7" {
		t.Fatalf("got parent span id %q", ctx.ParentSpanID)
	}
	if !ctx.Sampled {
		t.Fatal("expected sampled")
	}
	if len(ctx.SpanID) != 16 {
		t.Fatalf("got span id length %d", len(ctx.SpanID))
	}
}

func TestStartsANewRootTraceWhenHeaderIsAbsent(t *testing.T) {
	ctx := TraceContextFromHeaders(http.Header{})
	if len(ctx.TraceID) != 32 {
		t.Fatalf("got trace id length %d", len(ctx.TraceID))
	}
	if ctx.ParentSpanID != "" {
		t.Fatal("expected no parent span id")
	}
}

func TestRejectsMalformedHeadersAndStartsANewTraceInstead(t *testing.T) {
	h := http.Header{}
	h.Set("traceparent", "not-a-valid-traceparent")
	ctx := TraceContextFromHeaders(h)
	if ctx.ParentSpanID != "" {
		t.Fatal("expected no parent span id")
	}
}

func TestRejectsAllZeroTraceID(t *testing.T) {
	h := http.Header{}
	h.Set("traceparent", "00-00000000000000000000000000000000-00f067aa0ba902b7-01")
	ctx := TraceContextFromHeaders(h)
	if ctx.ParentSpanID != "" {
		t.Fatal("expected no parent span id")
	}
}

func TestToTraceparentHeaderRoundTrips(t *testing.T) {
	ctx := TraceContext{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7", Sampled: true}
	header := ctx.ToTraceparentHeader()
	traceID, parentID, sampled, ok := parseTraceparent(header)
	if !ok {
		t.Fatal("expected to parse")
	}
	if traceID != ctx.TraceID || parentID != ctx.SpanID || !sampled {
		t.Fatalf("got (%q, %q, %v)", traceID, parentID, sampled)
	}
}

func TestCurrentIsNoneOutsideAScope(t *testing.T) {
	if _, ok := CurrentTrace(context.Background()); ok {
		t.Fatal("expected not ok")
	}
}

func TestCurrentIsSomeInsideAScope(t *testing.T) {
	ctx := TraceContextFromHeaders(http.Header{})
	wrapped := WithTraceContext(context.Background(), ctx)
	got, ok := CurrentTrace(wrapped)
	if !ok || got.TraceID != ctx.TraceID {
		t.Fatalf("got (%v, %v)", got, ok)
	}
}
