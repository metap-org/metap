// Builds a query.ListInput from ListRequest.query — same defaults (limit 30, max 200) as REST's
// own list parsing, so a caller can't observe a difference in list semantics between the two
// transports. Ported from crates/metap-grpc/src/list_input.rs.
package grpcapi

import (
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap-go/query"
)

func listInputFromQuery(q map[string]any) (query.ListInput, error) {
	limit := int64(30)
	if n, ok := GetI64(q, "limit"); ok {
		if n <= 0 || n > 200 {
			return query.ListInput{}, status.Error(codes.InvalidArgument,
				"`limit` must be a positive integer no greater than 200.")
		}
		limit = n
	}

	var filters [][2]string
	if raw, ok := q["filter"]; ok {
		if obj, ok := raw.(map[string]any); ok {
			for k, v := range obj {
				filters = append(filters, [2]string{k, valueToFilterString(v)})
			}
		}
	}

	input := query.ListInput{Limit: limit, Filters: filters}
	if s, ok := GetStr(q, "sort"); ok {
		input.Sort = &s
	}
	if s, ok := GetStr(q, "cursor"); ok {
		input.Cursor = &s
	}
	if s, ok := GetStr(q, "listView"); ok {
		input.ListView = &s
	}
	if s, ok := GetStr(q, "jql"); ok {
		input.JQL = &s
	}
	return input, nil
}

// valueToFilterString mirrors the Rust value_to_filter_string: a non-string filter value coming
// through a Struct (numbers/bools are legal JSON there, unlike a real querystring) is coerced to
// its string form rather than rejected.
func valueToFilterString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
