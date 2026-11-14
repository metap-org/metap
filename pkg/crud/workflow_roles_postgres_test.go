//go:build integration

package crud

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
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

// ensureChangeRequestsTable: the table is created by newCrudService (it reconciles the entity), so
// there is nothing to hand-build any more; kept so callers read the same.
func ensureChangeRequestsTable(t *testing.T, pool *pgxpool.Pool) {}

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
	require.NoError(t, err)
	require.True(t, res.IsOk, "get failed: %d %s", res.Status, res.ErrorCode)
	out := map[string]TransitionAvailability{}
	for _, tr := range res.Data.Capabilities.Transitions {
		out[tr.Action] = tr
	}
	return out
}

func createDraft(t *testing.T, svc *CrudService, rc *permission.RequestContext) RecordDto {
	t.Helper()
	res, err := svc.Create(context.Background(), "test.change_requests", JsonObject{"title": raw("Block /wp-login.php")}, rc, nil)
	require.NoError(t, err)
	require.True(t, res.IsOk, "create failed: %d %s", res.Status, res.ErrorCode)
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
		_, err := perms.CreatePolicy(ctx, tenantID, "test.change_requests", action, roles, true, nil, nil, "", nil, permission.EffectAllow)
		require.NoError(t, err)
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
		require.True(t, got["publish"].Available, "publish should be available to a self-service author: %+v", got["publish"])
		require.False(t, got["submit"].Available, "submit should not be offered when the account needs no approval: %+v", got["submit"])
		res, err := svc.Transition(ctx, "test.change_requests", draft.ID, "publish", draft.Version, nil, author, nil)
		require.NoError(t, err)
		require.True(t, res.IsOk, "publish failed: %d %s", res.Status, res.ErrorCode)
		require.Equal(t, "published", *res.Data.Status, "publish failed: %d %s", res.Status, res.ErrorCode)
	})

	t.Run("enterprise author must submit; approver must be someone else", func(t *testing.T) {
		author := member(tenantID, true, "author", "approver") // holds both roles on purpose
		draft := createDraft(t, svc, author)

		got := availability(t, svc, draft.ID, author)
		require.False(t, got["publish"].Available, "enterprise author should see submit, not publish: %+v", got)
		require.True(t, got["submit"].Available, "enterprise author should see submit, not publish: %+v", got)
		require.NotNil(t, got["publish"].Reason, "publish should carry the guard message, got %+v", got["publish"].Reason)
		require.Equal(t, "This account requires approval; submit the change instead", *got["publish"].Reason, "publish should carry the guard message, got %+v", got["publish"].Reason)
		direct, err := svc.Transition(ctx, "test.change_requests", draft.ID, "publish", draft.Version, nil, author, nil)
		require.NoError(t, err)
		require.False(t, direct.IsOk, "direct publish should be refused by the guard, got ok=%v status=%d", direct.IsOk, direct.Status)
		require.EqualValues(t, 422, direct.Status, "direct publish should be refused by the guard, got ok=%v status=%d", direct.IsOk, direct.Status)

		submitted, err := svc.Transition(ctx, "test.change_requests", draft.ID, "submit", draft.Version, nil, author, nil)
		require.NoError(t, err)
		require.True(t, submitted.IsOk, "submit failed: %d %s", submitted.Status, submitted.ErrorCode)
		require.Equal(t, "pending", *submitted.Data.Status, "submit failed: %d %s", submitted.Status, submitted.ErrorCode)

		// Separation of duties: holding the approver role is not enough to approve one's own submission.
		self := availability(t, svc, draft.ID, author)
		require.False(t, self["approve"].Available, "the submitter must not be offered approve: %+v", self["approve"])
		require.NotNil(t, self["approve"].Reason, "approve should carry the guard message, got %+v", self["approve"].Reason)
		require.Equal(t, "The approver must be someone other than the submitter", *self["approve"].Reason, "approve should carry the guard message, got %+v", self["approve"].Reason)
		ownApproval, err := svc.Transition(ctx, "test.change_requests", draft.ID, "approve", submitted.Data.Version, nil, author, nil)
		require.NoError(t, err)
		require.False(t, ownApproval.IsOk, "self-approval should be refused, got ok=%v status=%d", ownApproval.IsOk, ownApproval.Status)
		require.EqualValues(t, 422, ownApproval.Status, "self-approval should be refused, got ok=%v status=%d", ownApproval.IsOk, ownApproval.Status)

		// A second author has no transition:approve policy: not offered, and refused at the entity level.
		otherAuthor := member(tenantID, true, "author")
		require.False(t, availability(t, svc, draft.ID, otherAuthor)["approve"].Available, "a caller without transition:approve must not be offered approve")
		noPolicy, err := svc.Transition(ctx, "test.change_requests", draft.ID, "approve", submitted.Data.Version, nil, otherAuthor, nil)
		require.NoError(t, err)
		require.False(t, noPolicy.IsOk, "approve without transition:approve should be 403, got ok=%v status=%d", noPolicy.IsOk, noPolicy.Status)
		require.EqualValues(t, 403, noPolicy.Status, "approve without transition:approve should be 403, got ok=%v status=%d", noPolicy.IsOk, noPolicy.Status)

		approver := member(tenantID, true, "approver")
		require.True(t, availability(t, svc, draft.ID, approver)["approve"].Available, "a different approver should be offered approve")
		approved, err := svc.Transition(ctx, "test.change_requests", draft.ID, "approve", submitted.Data.Version, nil, approver, nil)
		require.NoError(t, err)
		require.True(t, approved.IsOk, "approve by a different approver failed: %d %s", approved.Status, approved.ErrorCode)
		require.Equal(t, "published", *approved.Data.Status, "approve by a different approver failed: %d %s", approved.Status, approved.ErrorCode)
	})

	t.Run("a role with no transition policy is offered no transition", func(t *testing.T) {
		author := member(tenantID, true, "author")
		draft := createDraft(t, svc, author)
		viewer := member(tenantID, true, "approver") // can read, cannot submit or publish
		for action, tr := range availability(t, svc, draft.ID, viewer) {
			require.False(t, tr.Available, "%s should not be available to a caller without its policy", action)
		}
	})
}
