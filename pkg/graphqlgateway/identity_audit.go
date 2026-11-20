package graphqlgateway

import (
	"context"

	"github.com/graphql-go/graphql"
	"google.golang.org/grpc"

	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

var (
	auditEntryType = jsonObject("AuditEntry", map[string]graphql.Output{
		"id": nn(graphql.ID), "entity": nn(graphql.String), "recordId": nn(graphql.ID), "action": nn(graphql.String),
		"transitionAction": nn(graphql.String), "actorUserId": nn(graphql.String), "reason": nn(graphql.String),
		"occurredAt": nn(graphql.String), "diffJson": nn(graphql.String),
		// Position in the tenant's tamper-evident chain and its hex hash; 0 / "" for older history.
		"seq": nn(graphql.Int), "hash": nn(graphql.String),
	})
	auditPageType = jsonObject("AuditPage", map[string]graphql.Output{
		"entries": nnList(auditEntryType), "nextCursor": nn(graphql.String),
	})
	auditChainReportType = jsonObject("AuditChainReport", map[string]graphql.Output{
		"ok": nn(graphql.Boolean), "checked": nn(graphql.Int), "brokenSeq": nn(graphql.Int), "reason": nn(graphql.String),
		"headSeq": nn(graphql.Int), "headHash": nn(graphql.String),
	})
)

// addAuditFields exposes the tenant-wide audit search and chain check (roadmap 115 M9). The CSV
// / JSON export is REST (GET /admin/audit/export): a file download has no GraphQL shape.
func addAuditFields(query graphql.Fields, c *grpcapi.IdentityClient) {
	query["auditEntries"] = &graphql.Field{
		Type: nn(auditPageType),
		Args: graphql.FieldConfigArgument{
			"actorUserId": optStrArg(), "entity": optStrArg(), "recordId": optStrArg(),
			"actions": &graphql.ArgumentConfig{Type: graphql.NewList(graphql.NewNonNull(graphql.String))},
			"from":    optStrArg(), "to": optStrArg(), "cursor": optStrArg(),
			"limit": &graphql.ArgumentConfig{Type: graphql.Int},
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.SearchAuditRequest{ActorUserId: argString(p.Args, "actorUserId"), Entity: argString(p.Args, "entity"),
				RecordId: argString(p.Args, "recordId"), From: argString(p.Args, "from"), To: argString(p.Args, "to"),
				Cursor: argString(p.Args, "cursor"), Limit: int32Arg(p.Args, "limit")}
			if raw, ok := p.Args["actions"].([]interface{}); ok {
				for _, a := range raw {
					if s, ok := a.(string); ok {
						req.Actions = append(req.Actions, s)
					}
				}
			}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.SearchAuditResponse, error) {
				return c.Audit.SearchAudit(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			entries := make([]interface{}, len(res.GetEntries()))
			for i, e := range res.GetEntries() {
				entries[i] = map[string]interface{}{"id": e.GetId(), "entity": e.GetEntity(), "recordId": e.GetRecordId(),
					"action": e.GetAction(), "transitionAction": e.GetTransitionAction(), "actorUserId": e.GetActorUserId(),
					"reason": e.GetReason(), "occurredAt": e.GetOccurredAt(), "diffJson": e.GetDiffJson(),
					"seq": int(e.GetSeq()), "hash": e.GetHash()}
			}
			return map[string]interface{}{"entries": entries, "nextCursor": res.GetNextCursor()}, nil
		},
	}
	query["verifyAuditChain"] = &graphql.Field{
		Type: nn(auditChainReportType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			r, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.AuditChainReport, error) {
				return c.Audit.VerifyAuditChain(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"ok": r.GetOk(), "checked": int(r.GetChecked()), "brokenSeq": int(r.GetBrokenSeq()),
				"reason": r.GetReason(), "headSeq": int(r.GetHeadSeq()), "headHash": r.GetHeadHash()}, nil
		},
	}
}
