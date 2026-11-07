package audit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func sampleDiff() JsonObject {
	return JsonObject{
		"amount": json.RawMessage(`{"before":4242,"after":9999}`),
		"name":   json.RawMessage(`{"before":"old","after":"new"}`),
	}
}

func TestARedactedFieldKeepsTheFactItChangedButLosesBothValues(t *testing.T) {
	d := sampleDiff()
	RedactDiffFields(d, []string{"amount"})
	var got map[string]bool
	_ = json.Unmarshal(d["amount"], &got)
	require.True(t, got[RedactedMarker], "got %v", got)
	whole, _ := json.Marshal(d)
	require.NotContains(t, string(whole), "4242", "plaintext must be gone from the entry entirely")
	var name map[string]string
	_ = json.Unmarshal(d["name"], &name)
	require.Equal(t, "old", name["before"], "unlisted field must be untouched, got %v", name)
	require.Equal(t, "new", name["after"], "unlisted field must be untouched, got %v", name)
}

func TestAFieldAbsentFromTheDiffIsNotInvented(t *testing.T) {
	d := sampleDiff()
	RedactDiffFields(d, []string{"resolution"})
	_, ok := d["resolution"]
	require.False(t, ok, "must not invent a key")
	require.Len(t, d, 2, "got %d keys", len(d))
}

func TestRedactingNothingLeavesTheDiffAlone(t *testing.T) {
	d := sampleDiff()
	before, _ := json.Marshal(d)
	RedactDiffFields(d, []string{})
	after, _ := json.Marshal(d)
	require.Equal(t, string(after), string(before), "diff changed: %s -> %s", before, after)
}
