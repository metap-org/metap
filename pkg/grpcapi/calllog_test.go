package grpcapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcmd "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap/pkg/grpcapi/pb"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
)

// captureLog swaps the default logger for a JSON one writing into the returned buffer.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func lastLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	var m map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &m); err != nil {
		t.Fatalf("log line is not JSON: %v (%s)", err, buf.String())
	}
	return m
}

func TestCallLogNamesCallerEntityTenantAndPropagatesIDs(t *testing.T) {
	buf := captureLog(t)
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	ctx := grpcmd.NewIncomingContext(context.Background(), grpcmd.Pairs(
		standard.HeaderCaller, "waf-graphql-gateway",
		standard.HeaderRequestID, "req-123",
		standard.HeaderTraceparent, traceparent,
	))
	info := &grpc.UnaryServerInfo{FullMethod: "/metap.crud.v1.RecordService/Get"}

	var downstream grpcmd.MD
	handler := func(ctx context.Context, _ any) (any, error) {
		recordCaller(ctx, "tenant-1", "user-1")
		downstream = grpcmd.MD{}
		outgoingIDs(ctx, downstream)
		return nil, nil
	}
	if _, err := callLogInterceptor(ctx, &pb.GetRequest{EntityName: "waf.zones", Id: "z-1"}, info, handler); err != nil {
		t.Fatal(err)
	}

	line := lastLine(t, buf)
	want := map[string]any{
		"msg": "grpc request completed", "rpc": "RecordService/Get", "caller": "waf-graphql-gateway",
		"entity": "waf.zones", "record_id": "z-1", "tenant_id": "tenant-1", "user_id": "user-1",
		"code": "OK", "request_id": "req-123", "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
		"parent_span_id": "00f067aa0ba902b7",
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("%s = %v, want %v", k, line[k], v)
		}
	}

	// A call made while serving this one carries the same request and trace on, as this service.
	if got := firstMD(downstream, standard.HeaderRequestID); got != "req-123" {
		t.Errorf("downstream request id = %q", got)
	}
	tc := httpx.TraceContextFromHeaders(map[string][]string{"Traceparent": {firstMD(downstream, standard.HeaderTraceparent)}})
	if tc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || tc.ParentSpanID != line["span_id"] {
		t.Errorf("downstream traceparent = %q, want trace kept and parent = this span %v", firstMD(downstream, standard.HeaderTraceparent), line["span_id"])
	}
	if firstMD(downstream, standard.HeaderCaller) != callerName() {
		t.Errorf("downstream caller = %q", firstMD(downstream, standard.HeaderCaller))
	}
}

func TestCallLogLevelsByCode(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/metap.crud.v1.ActionService/Invoke"}
	cases := []struct {
		code      codes.Code
		wantLevel string
		wantMsg   string
	}{
		{codes.NotFound, "WARN", "grpc request rejected"},
		{codes.Internal, "ERROR", "grpc request failed"},
	}
	for _, c := range cases {
		buf := captureLog(t)
		handler := func(context.Context, any) (any, error) { return nil, status.Error(c.code, "boom") }
		req := &pb.InvokeRequest{EntityName: "waf.zones", Action: "verifyDns", Id: "z-1"}
		_, _ = callLogInterceptor(context.Background(), req, info, handler)
		line := lastLine(t, buf)
		if line["level"] != c.wantLevel || line["msg"] != c.wantMsg || line["action"] != "verifyDns" || line["caller"] != "unknown" {
			t.Errorf("%v: got %v", c.code, line)
		}
	}
}

func TestShortMethod(t *testing.T) {
	if got := shortMethod("/metap.crud.v1.RecordService/List"); got != "RecordService/List" {
		t.Fatal(got)
	}
}
