package audit

import (
	"encoding/json"
	"strings"
	"testing"
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
	if !got[RedactedMarker] {
		t.Fatalf("got %v", got)
	}
	whole, _ := json.Marshal(d)
	if strings.Contains(string(whole), "4242") {
		t.Fatal("plaintext must be gone from the entry entirely")
	}
	var name map[string]string
	_ = json.Unmarshal(d["name"], &name)
	if name["before"] != "old" || name["after"] != "new" {
		t.Fatalf("unlisted field must be untouched, got %v", name)
	}
}

func TestAFieldAbsentFromTheDiffIsNotInvented(t *testing.T) {
	d := sampleDiff()
	RedactDiffFields(d, []string{"resolution"})
	if _, ok := d["resolution"]; ok {
		t.Fatal("must not invent a key")
	}
	if len(d) != 2 {
		t.Fatalf("got %d keys", len(d))
	}
}

func TestRedactingNothingLeavesTheDiffAlone(t *testing.T) {
	d := sampleDiff()
	before, _ := json.Marshal(d)
	RedactDiffFields(d, []string{})
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatalf("diff changed: %s -> %s", before, after)
	}
}
