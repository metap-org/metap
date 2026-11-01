package runtime

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// TraceContext is W3C Trace Context (https://www.w3.org/TR/trace-context/) propagation — lets
// this service interoperate with a mesh sidecar (Istio/Envoy, Linkerd) without a full OpenTelemetry
// SDK. Ambient via context.Context (ported from the Rust tokio::task_local!), so any function
// called with the request's ctx can read it back via CurrentTrace(ctx).
type TraceContext struct {
	TraceID      string // 32 hex chars, fixed for the whole call chain
	ParentSpanID string // caller's own span id, "" if absent/invalid
	SpanID       string // freshly generated for this hop
	Sampled      bool
}

func newTraceID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

func newSpanID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

func allZero(s string) bool {
	for _, c := range s {
		if c != '0' {
			return false
		}
	}
	return true
}

// parseTraceparent parses "version-traceid-parentid-flags", e.g.
// "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01". Only version 00 is defined; an
// all-zero trace-id/parent-id is explicitly invalid per spec.
func parseTraceparent(header string) (traceID, parentID string, sampled bool, ok bool) {
	parts := strings.Split(header, "-")
	if len(parts) != 4 {
		return "", "", false, false
	}
	version, traceID, parentID, flags := parts[0], parts[1], parts[2], parts[3]
	if len(version) != 2 || len(traceID) != 32 || len(parentID) != 16 || len(flags) != 2 {
		return "", "", false, false
	}
	if !isHex(version) || !isHex(traceID) || !isHex(parentID) || !isHex(flags) {
		return "", "", false, false
	}
	if allZero(traceID) || allZero(parentID) {
		return "", "", false, false
	}
	flagsByte, err := hex.DecodeString(flags)
	if err != nil {
		return "", "", false, false
	}
	return traceID, parentID, flagsByte[0]&0x01 != 0, true
}

// TraceContextFromHeaders parses the incoming traceparent header if present and well-formed;
// otherwise starts a new root trace.
func TraceContextFromHeaders(h http.Header) TraceContext {
	if traceID, parentID, sampled, ok := parseTraceparent(h.Get("traceparent")); ok {
		return TraceContext{TraceID: traceID, ParentSpanID: parentID, SpanID: newSpanID(), Sampled: sampled}
	}
	return TraceContext{TraceID: newTraceID(), SpanID: newSpanID(), Sampled: true}
}

// ToTraceparentHeader builds the traceparent value to send on an outbound call.
func (c TraceContext) ToTraceparentHeader() string {
	flags := "00"
	if c.Sampled {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", c.TraceID, c.SpanID, flags)
}

type traceContextKey struct{}

// WithTraceContext makes ctx ambient for current()-style lookups via CurrentTrace.
func WithTraceContext(ctx context.Context, tc TraceContext) context.Context {
	return context.WithValue(ctx, traceContextKey{}, tc)
}

// CurrentTrace returns the ambient TraceContext, or false outside of WithTraceContext.
func CurrentTrace(ctx context.Context) (TraceContext, bool) {
	tc, ok := ctx.Value(traceContextKey{}).(TraceContext)
	return tc, ok
}
