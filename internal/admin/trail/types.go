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

func (x *AuditChainReport) GetOk() bool {
	if x != nil {
		return x.Ok
	}
	return false
}

func (x *AuditChainReport) GetChecked() int64 {
	if x != nil {
		return x.Checked
	}
	return 0
}

func (x *AuditChainReport) GetBrokenSeq() int64 {
	if x != nil {
		return x.BrokenSeq
	}
	return 0
}

func (x *AuditChainReport) GetReason() string {
	if x != nil {
		return x.Reason
	}
	return ""
}

func (x *AuditChainReport) GetHeadSeq() int64 {
	if x != nil {
		return x.HeadSeq
	}
	return 0
}

func (x *AuditChainReport) GetHeadHash() string {
	if x != nil {
		return x.HeadHash
	}
	return ""
}

func (x *SearchAuditResponse) GetEntries() []*AuditEntry {
	if x != nil {
		return x.Entries
	}
	return nil
}

func (x *SearchAuditResponse) GetNextCursor() string {
	if x != nil {
		return x.NextCursor
	}
	return ""
}

func (x *AuditEntry) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *AuditEntry) GetEntity() string {
	if x != nil {
		return x.Entity
	}
	return ""
}

func (x *AuditEntry) GetRecordId() string {
	if x != nil {
		return x.RecordId
	}
	return ""
}

func (x *AuditEntry) GetAction() string {
	if x != nil {
		return x.Action
	}
	return ""
}

func (x *AuditEntry) GetTransitionAction() string {
	if x != nil {
		return x.TransitionAction
	}
	return ""
}

func (x *AuditEntry) GetActorUserId() string {
	if x != nil {
		return x.ActorUserId
	}
	return ""
}

func (x *AuditEntry) GetReason() string {
	if x != nil {
		return x.Reason
	}
	return ""
}

func (x *AuditEntry) GetOccurredAt() string {
	if x != nil {
		return x.OccurredAt
	}
	return ""
}

func (x *AuditEntry) GetDiffJson() string {
	if x != nil {
		return x.DiffJson
	}
	return ""
}

func (x *AuditEntry) GetSeq() int64 {
	if x != nil {
		return x.Seq
	}
	return 0
}

func (x *AuditEntry) GetHash() string {
	if x != nil {
		return x.Hash
	}
	return ""
}

func (x *SearchAuditRequest) GetActorUserId() string {
	if x != nil {
		return x.ActorUserId
	}
	return ""
}

func (x *SearchAuditRequest) GetEntity() string {
	if x != nil {
		return x.Entity
	}
	return ""
}

func (x *SearchAuditRequest) GetRecordId() string {
	if x != nil {
		return x.RecordId
	}
	return ""
}

func (x *SearchAuditRequest) GetActions() []string {
	if x != nil {
		return x.Actions
	}
	return nil
}

func (x *SearchAuditRequest) GetFrom() string {
	if x != nil {
		return x.From
	}
	return ""
}

func (x *SearchAuditRequest) GetTo() string {
	if x != nil {
		return x.To
	}
	return ""
}

func (x *SearchAuditRequest) GetCursor() string {
	if x != nil {
		return x.Cursor
	}
	return ""
}

func (x *SearchAuditRequest) GetLimit() int32 {
	if x != nil {
		return x.Limit
	}
	return 0
}
