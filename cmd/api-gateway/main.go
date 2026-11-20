// api-gateway is pkg/apigateway as a binary (roadmap 116): the HTTP front of a product's services,
// replacing the GraphQL gateway. Configuration: see apigateway.Main.
package main

import "github.com/metap-org/metap/pkg/apigateway"

func main() { apigateway.Main() }
