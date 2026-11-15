// Access logging and context propagation for the gRPC listener. RPC method names are generic
// (every entity goes through "/metap.crud.v1.RecordService/List"), so a line with only the method
// says nothing about who called, for which entity, or as which tenant. callLogInterceptor records
// all of that, and makes the incoming request/trace ids ambient so the handler's own downstream
// calls (a cross-service gRPC, an outbound HTTP) carry the same ids on.
package grpcapi

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcmd "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
)

// callerName is what this process sends as HeaderCaller on every outbound RPC: SERVICE_NAME when
// set, else the binary's own name (cmd/<name> builds, and `go run`, both name it after the
// package directory, e.g. "waf-graphql-gateway").
var callerName = sync.OnceValue(func() string {
	if v := env.String("SERVICE_NAME", ""); v != "" {
		return v
	}
	return filepath.Base(os.Args[0])
})

// callInfo is filled in by authenticate while the handler runs — the interceptor cannot know the
// tenant/user itself, since resolving them is the handler's first step.
type callInfo struct {
	tenantID string
	userID   string
}

type callInfoKey struct{}

func recordCaller(ctx context.Context, tenantID, userID string) {
	if ci, ok := ctx.Value(callInfoKey{}).(*callInfo); ok {
		ci.tenantID, ci.userID = tenantID, userID
	}
}

// Every request message names its entity, and most name a record id and/or an action; the
// generated getters let the interceptor read them without a type switch per RPC.
type (
	entityNamed interface{ GetEntityName() string }
	idNamed     interface{ GetId() string }
	actionNamed interface{ GetAction() string }
)

func firstMD(md grpcmd.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// incomingContext makes the caller's request/trace ids ambient (minting them when absent), the
// same way httpx.GenerateRequestIDs does for an HTTP request, and returns the caller's name.
func incomingContext(ctx context.Context) (context.Context, string) {
	md, _ := grpcmd.FromIncomingContext(ctx)
	ids := httpx.RequestIDs{
		RequestID: httpx.OpaqueIDOrNew(firstMD(md, standard.HeaderRequestID)),
		TraceID:   httpx.OpaqueIDOrNew(firstMD(md, standard.HeaderLegacyTraceID)),
	}
	h := http.Header{}
	if tp := firstMD(md, standard.HeaderTraceparent); tp != "" {
		h.Set(standard.HeaderTraceparent, tp)
	}
	ctx = httpx.WithRequestIDs(ctx, ids)
	ctx = httpx.WithTraceContext(ctx, httpx.TraceContextFromHeaders(h))

	caller := firstMD(md, standard.HeaderCaller)
	if caller == "" {
		// Not a metap client (grpcurl, another stack): its user agent is the best name there is.
		caller = firstMD(md, "user-agent")
	}
	if caller == "" {
		caller = "unknown"
	}
	return ctx, caller
}

// callLogInterceptor logs one line per RPC: what was asked (rpc, entity, record id, action), who
// asked (calling service, peer address, tenant, user), how it ended (gRPC code) and the ids that
// tie it to the caller's own log lines.
func callLogInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	start := time.Now()
	ctx, caller := incomingContext(ctx)
	ci := &callInfo{}
	ctx = context.WithValue(ctx, callInfoKey{}, ci)

	resp, err := handler(ctx, req)

	code := status.Code(err)
	attrs := []any{
		"rpc", shortMethod(info.FullMethod),
		standard.LogCaller, caller,
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		attrs = append(attrs, "peer", p.Addr.String())
	}
	if r, ok := req.(entityNamed); ok && r.GetEntityName() != "" {
		attrs = append(attrs, standard.LogEntity, r.GetEntityName())
	}
	if r, ok := req.(idNamed); ok && r.GetId() != "" {
		attrs = append(attrs, standard.LogRecordID, r.GetId())
	}
	if r, ok := req.(actionNamed); ok && r.GetAction() != "" {
		attrs = append(attrs, standard.LogAction, r.GetAction())
	}
	if ci.tenantID != "" {
		attrs = append(attrs, standard.LogTenantID, ci.tenantID, standard.LogUserID, ci.userID)
	}
	ids, _ := httpx.CurrentRequestIDs(ctx)
	tc, _ := httpx.CurrentTrace(ctx)
	attrs = append(attrs,
		"code", code.String(),
		standard.LogRequestID, ids.RequestID,
		standard.LogTraceID, tc.TraceID,
		standard.LogSpanID, tc.SpanID,
		standard.LogLatencyMs, time.Since(start).Milliseconds(),
	)
	if tc.ParentSpanID != "" {
		attrs = append(attrs, "parent_span_id", tc.ParentSpanID)
	}

	switch {
	case err == nil:
		slog.Info("grpc request completed", attrs...)
	case isServerFault(code):
		slog.Error("grpc request failed", append(attrs, standard.LogError, err)...)
	default:
		// A refused or invalid call (not found, denied, conflict...) is the caller's problem,
		// not an incident on this service.
		slog.Warn("grpc request rejected", append(attrs, standard.LogError, status.Convert(err).Message())...)
	}
	return resp, err
}

func isServerFault(code codes.Code) bool {
	switch code {
	case codes.Internal, codes.Unknown, codes.Unavailable, codes.DataLoss, codes.DeadlineExceeded, codes.Unimplemented:
		return true
	}
	return false
}

// shortMethod turns "/metap.crud.v1.RecordService/List" into "RecordService/List".
func shortMethod(full string) string {
	full = strings.TrimPrefix(full, "/")
	svc, method, ok := strings.Cut(full, "/")
	if !ok {
		return full
	}
	if i := strings.LastIndex(svc, "."); i >= 0 {
		svc = svc[i+1:]
	}
	return svc + "/" + method
}

// outgoingIDs appends what a downstream metap service needs to log this call as coming from us,
// in the same request/trace: caller name, request ids, W3C traceparent.
func outgoingIDs(ctx context.Context, md grpcmd.MD) {
	md.Append(standard.HeaderCaller, callerName())
	if ids, ok := httpx.CurrentRequestIDs(ctx); ok {
		if ids.RequestID != "" {
			md.Append(standard.HeaderRequestID, ids.RequestID)
		}
		if ids.TraceID != "" {
			md.Append(standard.HeaderLegacyTraceID, ids.TraceID)
		}
	}
	if tc, ok := httpx.CurrentTrace(ctx); ok {
		md.Append(standard.HeaderTraceparent, tc.ToTraceparentHeader())
	}
}
