package apigateway

import (
	"log/slog"
	"os"

	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
)

// Main runs the gateway as a process, configured from the environment (a product's own gateway
// binary is just this):
//
//	LISTEN_ADDR  default 0.0.0.0:4000
//	UPSTREAMS    name=baseURL[|prefix,prefix];...  (see ParseUpstreams)
func Main() {
	addr := env.String("LISTEN_ADDR", "0.0.0.0:4000")
	upstreams, err := ParseUpstreams(os.Getenv("UPSTREAMS"))
	if err != nil {
		slog.Error("configuring upstreams", standard.LogError, err)
		os.Exit(1)
	}
	gw, err := New(Config{Upstreams: upstreams})
	if err != nil {
		slog.Error("building gateway", standard.LogError, err)
		os.Exit(1)
	}
	for _, u := range upstreams {
		slog.Info("upstream", "name", u.Name, "base_url", u.BaseURL, "prefixes", u.Prefixes)
	}
	slog.Info("api-gateway listening", "addr", addr)
	if err := httpx.Serve(addr, httpx.Recover(gw)); err != nil {
		slog.Error("serving", standard.LogError, err)
		os.Exit(1)
	}
}
