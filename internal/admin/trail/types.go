package trail

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

type SearchAuditRequest struct {
	ActorUserId string   `json:"actorUserId" required:"false"`
	Entity      string   `json:"entity" required:"false"`
	RecordId    string   `json:"recordId" required:"false"`
	Actions     []string `json:"actions" required:"false"`
	From        string   `json:"from" required:"false"`
	To          string   `json:"to" required:"false"`
	Cursor      string   `json:"cursor" required:"false"`
	Limit       int32    `json:"limit" required:"false"`
}

type AuditEntry struct {
	Id               string `json:"id" required:"false"`
	Entity           string `json:"entity" required:"false"`
	RecordId         string `json:"recordId" required:"false"`
	Action           string `json:"action" required:"false"`
	TransitionAction string `json:"transitionAction" required:"false"`
	ActorUserId      string `json:"actorUserId" required:"false"`
	Reason           string `json:"reason" required:"false"`
	OccurredAt       string `json:"occurredAt" required:"false"`
	DiffJson         string `json:"diffJson" required:"false"`
	// Position in the tenant's chain and its hash (hex); 0/"" for history from before the chain.
	Seq  int64  `json:"seq" required:"false"`
	Hash string `json:"hash" required:"false"`
}

type SearchAuditResponse struct {
	Entries    []*AuditEntry `json:"entries" required:"false"`
	NextCursor string        `json:"nextCursor" required:"false"`
}

type AuditChainReport struct {
	Ok      bool  `json:"ok" required:"false"`
	Checked int64 `json:"checked" required:"false"`
	// The first position that does not verify, and why: "gap", "prev_hash", "hash" or "head".
	BrokenSeq int64  `json:"brokenSeq" required:"false"`
	Reason    string `json:"reason" required:"false"`
	// The chain's recorded end; keep head_hash somewhere the database owner cannot rewrite to
	// check the chain against an outside witness.
	HeadSeq  int64  `json:"headSeq" required:"false"`
	HeadHash string `json:"headHash" required:"false"`
}
