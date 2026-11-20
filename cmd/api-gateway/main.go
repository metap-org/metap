// api-gateway is pkg/apigateway as a binary (roadmap 116): the HTTP front of a product's services,
// replacing the GraphQL gateway.
//
//	LISTEN_ADDR  default 0.0.0.0:4000
//	UPSTREAMS    name=baseURL[|prefix,prefix];...  (see apigateway.ParseUpstreams)
package main

import (
	"log/slog"
	"os"

	"github.com/metap-org/metap/pkg/apigateway"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
)

func main() {
	addr := env.String("LISTEN_ADDR", "0.0.0.0:4000")
	upstreams, err := apigateway.ParseUpstreams(os.Getenv("UPSTREAMS"))
	if err != nil {
		slog.Error("configuring upstreams", standard.LogError, err)
		os.Exit(1)
	}
	gw, err := apigateway.New(apigateway.Config{Upstreams: upstreams})
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
