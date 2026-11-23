package apigateway

import (
	"log/slog"
	"os"
	"time"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/runtime"
)

// Main runs the gateway as a process, configured from the environment (a product's own gateway
// binary is just this):
//
//	LISTEN_ADDR  default 0.0.0.0:4000
//	UPSTREAMS    name=baseURL[|prefix,prefix];...  (see ParseUpstreams)
//	TRUSTED_PROXIES  proxies in front of the gateway (CIDRs or IPs, comma separated) whose
//	                 X-Forwarded-For is believed; unset: the TCP peer is the caller
//	RATE_LIMIT_PER_MS, RATE_LIMIT_BURST  per-caller token bucket (default 200 ms, burst 300)
func Main() {
	if err := config.LoadFileFromEnv(); err != nil {
		slog.Error("loading the config file", standard.LogError, err)
		os.Exit(1)
	}
	addr := env.String("LISTEN_ADDR", "0.0.0.0:4000")
	upstreams, err := ParseUpstreams(env.String("UPSTREAMS", ""))
	if err != nil {
		slog.Error("configuring upstreams", standard.LogError, err)
		os.Exit(1)
	}
	trusted, err := httpx.ParseTrustedProxies(env.List("TRUSTED_PROXIES"))
	if err != nil {
		slog.Error("configuring trusted proxies", standard.LogError, err)
		os.Exit(1)
	}
	gw, err := New(Config{Upstreams: upstreams, TrustedProxies: trusted})
	if err != nil {
		slog.Error("building gateway", standard.LogError, err)
		os.Exit(1)
	}
	for _, u := range upstreams {
		slog.Info("upstream", "name", u.Name, "base_url", u.BaseURL, "prefixes", u.Prefixes)
	}
	slog.Info("api-gateway listening", "addr", addr)
	// The gateway is where callers are told apart, so it is where they are rate limited: the
	// services behind it (which trust it via TRUSTED_PROXIES) apply the same per-caller limit.
	limit := runtime.RateLimitBehind(
		time.Duration(env.Int("RATE_LIMIT_PER_MS", 200))*time.Millisecond, env.Int("RATE_LIMIT_BURST", 300), trusted)
	if err := httpx.Serve(addr, httpx.Recover(limit(gw))); err != nil {
		slog.Error("serving", standard.LogError, err)
		os.Exit(1)
	}
}
