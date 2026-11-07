package httpx_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/kit/httpx"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

func TestServeContextServesUntilCancelledThenShutsDownCleanly(t *testing.T) {
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	server := &http.Server{Addr: addr, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})}
	go func() { done <- httpx.ServeContext(ctx, server, time.Second) }()

	client := httpx.NewClient(2 * time.Second)
	var body []byte
	require.Eventually(t, func() bool {
		res, err := client.Get("http://" + addr)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		body, _ = io.ReadAll(res.Body)
		return true
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, "ok", string(body))

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		require.FailNow(t, "ServeContext did not return after cancellation")
	}
}

func TestServeContextReturnsTheListenErrorImmediately(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()

	err = httpx.ServeContext(context.Background(), &http.Server{Addr: l.Addr().String()}, time.Second)
	require.Error(t, err) // address already in use
}

func TestDefaultClientAlwaysHasATimeout(t *testing.T) {
	assert.Equal(t, httpx.DefaultClientTimeout, httpx.DefaultClient().Timeout)
}
