package crud

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
)

const changeRequestsTable = "entities.test_change_requests"

// changeRequestEntity is one workflow serving two kinds of account:
//
//   - self-service (context attribute approvalRequired = false): the author publishes directly;
//   - enterprise (approvalRequired = true): the author submits, someone else approves.
//
// Which path a caller sees comes only from guards and per-action policies — no second entity.
func changeRequestEntity() metadata.EntityDefinition {
	selfService := permission.AttributeCondition("$ctx.approvalRequired", permission.OpNeq, permission.LiteralValue(raw(true)))
	needsApproval := permission.AttributeCondition("$ctx.approvalRequired", permission.OpEq, permission.LiteralValue(raw(true)))
	notSubmitter := permission.AttributeCondition("$workflow.actors.submit", permission.OpNeq, permission.FromContextValue("userId"))
	return metadata.EntityDefinition{
		Name: "test.change_requests", Label: "Change request", TableName: changeRequestsTable,
		Fields: []metadata.EntityField{
			{Name: "title", Label: "Title", Kind: metadata.KindString, Required: boolPtr(true)},
			{Name: "status", Label: "Status", Kind: metadata.KindEnum, EnumValues: []string{"draft", "pending", "published"}},
		},
		Workflow: &metadata.EntityWorkflow{
			StateField: "status", InitialState: "draft", TerminalStates: []string{"published"},
			Transitions: []metadata.WorkflowTransition{
				{Action: "publish", From: "draft", To: "published", Label: "Publish", Guard: &selfService,
					GuardMessage: "This account requires approval; submit the change instead"},
				{Action: "submit", From: "draft", To: "pending", Label: "Submit for approval", Guard: &needsApproval,
					GuardMessage: "This account does not require approval; publish directly"},
				{Action: "approve", From: "pending", To: "published", Label: "Approve", Guard: &notSubmitter,
					GuardMessage: "The approver must be someone other than the submitter"},
			},
		},
	}
}

func ensureChangeRequestsTable(t *testing.T, pool *pgxpool.Pool) {
	_, err := pool.Exec(context.Background(), `CREATE TABLE IF NOT EXISTS `+changeRequestsTable+` (
		id uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
		tenant_id uuid NOT NULL,
		code varchar(120),
		status varchar(80),
		data jsonb DEFAULT '{}'::jsonb NOT NULL,
		version integer DEFAULT 1 NOT NULL,
		deleted boolean DEFAULT false NOT NULL,
		created_at timestamptz DEFAULT now() NOT NULL,
		updated_at timestamptz DEFAULT now() NOT NULL,
		created_by uuid,
		updated_by uuid
	)`)
	if err != nil {
		t.Fatal(err)
	}
}

// member is a non-admin caller of one account; approvalRequired models the account's mode.
func member(tenantID uuid.UUID, approvalRequired bool, roles ...string) *permission.RequestContext {
	return &permission.RequestContext{
		TenantID: tenantID.String(),
		UserID:   uuid.New().String(),
		Roles:    roles,
		HasRoles: true,
		ContextAttributes: map[string]json.RawMessage{
			"approvalRequired": raw(approvalRequired),
		},
	}
}

func availability(t *testing.T, svc *CrudService, id uuid.UUID, rc *permission.RequestContext) map[string]TransitionAvailability {
	t.Helper()
	res, err := svc.Get(context.Background(), "test.change_requests", id, rc)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsOk {
		t.Fatalf("get failed: %d %s", res.Status, res.ErrorCode)
	}
	out := map[string]TransitionAvailability{}
	for _, tr := range res.Data.Capabilities.Transitions {
		out[tr.Action] = tr
	}
	return out
}

func createDraft(t *testing.T, svc *CrudService, rc *permission.RequestContext) RecordDto {
	t.Helper()
	res, err := svc.Create(context.Background(), "test.change_requests", JsonObject{"title": raw("Block /wp-login.php")}, rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsOk {
		t.Fatalf("create failed: %d %s", res.Status, res.ErrorCode)
	}
	return res.Data
}

func TestOneWorkflowShowsEachCallerOnlyTheTransitionsTheyCanTake(t *testing.T) {
	pool := testPool(t)
	ensureChangeRequestsTable(t, pool)
	ctx := context.Background()
	tenantID := uuid.New()
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM metadata.outbox_events WHERE aggregate_id IN (SELECT id FROM `+changeRequestsTable+` WHERE tenant_id = $1)`, tenantID)
		pool.Exec(ctx, `DELETE FROM metadata.workflow_events WHERE tenant_id = $1`, tenantID)
		pool.Exec(ctx, `DELETE FROM `+changeRequestsTable+` WHERE tenant_id = $1`, tenantID)
		pool.Exec(ctx, `DELETE FROM metadata.policies WHERE tenant_id = $1`, tenantID)
	})

	perms := testPermissions(pool)
	grant := func(action string, roles ...string) {
		if _, err := perms.CreatePolicy(ctx, tenantID, "test.change_requests", action, roles, true, nil, nil, "", nil, permission.EffectAllow); err != nil {
			t.Fatal(err)
		}
	}
	grant("read", "author", "approver")
	grant("create", "author")
	grant(permission.TransitionActionPolicy("publish"), "author")
	grant(permission.TransitionActionPolicy("submit"), "author")
	grant(permission.TransitionActionPolicy("approve"), "approver")

	svc := newCrudService(pool, changeRequestEntity())

	t.Run("self-service author publishes without anyone approving", func(t *testing.T) {
		author := member(tenantID, false, "author")
		draft := createDraft(t, svc, author)
		got := availability(t, svc, draft.ID, author)
		if !got["publish"].Available {
			t.Fatalf("publish should be available to a self-service author: %+v", got["publish"])
		}
		if got["submit"].Available {
			t.Fatalf("submit should not be offered when the account needs no approval: %+v", got["submit"])
		}
		res, err := svc.Transition(ctx, "test.change_requests", draft.ID, "publish", draft.Version, nil, author, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsOk || *res.Data.Status != "published" {
			t.Fatalf("publish failed: %d %s", res.Status, res.ErrorCode)
		}
	})

	t.Run("enterprise author must submit; approver must be someone else", func(t *testing.T) {
		author := member(tenantID, true, "author", "approver") // holds both roles on purpose
		draft := createDraft(t, svc, author)

		got := availability(t, svc, draft.ID, author)
		if got["publish"].Available || !got["submit"].Available {
			t.Fatalf("enterprise author should see submit, not publish: %+v", got)
		}
		if got["publish"].Reason == nil || *got["publish"].Reason != "This account requires approval; submit the change instead" {
			t.Fatalf("publish should carry the guard message, got %+v", got["publish"].Reason)
		}
		direct, err := svc.Transition(ctx, "test.change_requests", draft.ID, "publish", draft.Version, nil, author, nil)
		if err != nil {
			t.Fatal(err)
		}
		if direct.IsOk || direct.Status != 422 {
			t.Fatalf("direct publish should be refused by the guard, got ok=%v status=%d", direct.IsOk, direct.Status)
		}

		submitted, err := svc.Transition(ctx, "test.change_requests", draft.ID, "submit", draft.Version, nil, author, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !submitted.IsOk || *submitted.Data.Status != "pending" {
			t.Fatalf("submit failed: %d %s", submitted.Status, submitted.ErrorCode)
		}

		// Separation of duties: holding the approver role is not enough to approve one's own submission.
		self := availability(t, svc, draft.ID, author)
		if self["approve"].Available {
			t.Fatalf("the submitter must not be offered approve: %+v", self["approve"])
		}
		if self["approve"].Reason == nil || *self["approve"].Reason != "The approver must be someone other than the submitter" {
			t.Fatalf("approve should carry the guard message, got %+v", self["approve"].Reason)
		}
		ownApproval, err := svc.Transition(ctx, "test.change_requests", draft.ID, "approve", submitted.Data.Version, nil, author, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ownApproval.IsOk || ownApproval.Status != 422 {
			t.Fatalf("self-approval should be refused, got ok=%v status=%d", ownApproval.IsOk, ownApproval.Status)
		}

		// A second author has no transition:approve policy: not offered, and refused at the entity level.
		otherAuthor := member(tenantID, true, "author")
		if availability(t, svc, draft.ID, otherAuthor)["approve"].Available {
			t.Fatal("a caller without transition:approve must not be offered approve")
		}
		noPolicy, err := svc.Transition(ctx, "test.change_requests", draft.ID, "approve", submitted.Data.Version, nil, otherAuthor, nil)
		if err != nil {
			t.Fatal(err)
		}
		if noPolicy.IsOk || noPolicy.Status != 403 {
			t.Fatalf("approve without transition:approve should be 403, got ok=%v status=%d", noPolicy.IsOk, noPolicy.Status)
		}

		approver := member(tenantID, true, "approver")
		if !availability(t, svc, draft.ID, approver)["approve"].Available {
			t.Fatal("a different approver should be offered approve")
		}
		approved, err := svc.Transition(ctx, "test.change_requests", draft.ID, "approve", submitted.Data.Version, nil, approver, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !approved.IsOk || *approved.Data.Status != "published" {
			t.Fatalf("approve by a different approver failed: %d %s", approved.Status, approved.ErrorCode)
		}
	})

	t.Run("a role with no transition policy is offered no transition", func(t *testing.T) {
		author := member(tenantID, true, "author")
		draft := createDraft(t, svc, author)
		viewer := member(tenantID, true, "approver") // can read, cannot submit or publish
		for action, tr := range availability(t, svc, draft.ID, viewer) {
			if tr.Available {
				t.Fatalf("%s should not be available to a caller without its policy", action)
			}
		}
	})
}
