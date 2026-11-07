package grpcapi

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func echoHandler(ctx context.Context, req any) (any, error) {
	return "ok", nil
}

func ctxWithPeer(ip string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 12345}})
}

func TestRateLimitAllowsCallsWithinTheBurstThenRejectsTheNextOne(t *testing.T) {
	limiter := newRateLimiter(time.Hour, 3)
	interceptor := limiter.UnaryInterceptor()
	ctx := ctxWithPeer("127.0.0.1")
	info := &grpc.UnaryServerInfo{}

	for i := 0; i < 3; i++ {
		_, err := interceptor(ctx, nil, info, echoHandler)
		require.NoError(t, err, "burst call %d should reach the handler, got", i)
	}
	_, err := interceptor(ctx, nil, info, echoHandler)
	require.Error(t, err, "the 4th call within the burst window must be rejected")
	st, _ := status.FromError(err)
	if st.Code() != codes.ResourceExhausted {
		require.FailNow(t, fmt.Sprintf("got %v", st.Code()))
	}
}

func TestRateLimitTracksSeparatePeersIndependently(t *testing.T) {
	limiter := newRateLimiter(time.Hour, 1)
	interceptor := limiter.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{}

	if _, err := interceptor(ctxWithPeer("10.0.0.1"), nil, info, echoHandler); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	// a is now exhausted, but b has never been charged — must not share a's bucket.
	if _, err := interceptor(ctxWithPeer("10.0.0.2"), nil, info, echoHandler); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	_, err := interceptor(ctxWithPeer("10.0.0.1"), nil, info, echoHandler)
	require.Error(t, err, "expected a's second call to be rejected")
	st, _ := status.FromError(err)
	if st.Code() != codes.ResourceExhausted {
		require.FailNow(t, fmt.Sprintf("got %v", st.Code()))
	}
}

func TestRateLimitFailsOpenWhenNoPeerIsPresent(t *testing.T) {
	limiter := newRateLimiter(time.Hour, 1)
	interceptor := limiter.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{}

	for i := 0; i < 5; i++ {
		_, err := interceptor(context.Background(), nil, info, echoHandler)
		require.NoError(t, err, "call %d: expected fail-open with no peer info, got", i)
	}
}
