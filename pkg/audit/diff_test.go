package audit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func obj(pairs map[string]string) JsonObject {
	out := JsonObject{}
	for k, v := range pairs {
		out[k] = json.RawMessage(v)
	}
	return out
}

func TestNoDiffWhenObjectsAreEqual(t *testing.T) {
	before := obj(map[string]string{"name": `"Alice"`, "age": "30"})
	after := obj(map[string]string{"name": `"Alice"`, "age": "30"})
	d := DiffJSONObjects(before, after)
	require.Len(t, d, 0, "expected empty diff, got %v", d)
}

func TestReportsAChangedKey(t *testing.T) {
	before := obj(map[string]string{"name": `"Alice"`})
	after := obj(map[string]string{"name": `"Bob"`})
	d := DiffJSONObjects(before, after)
	require.Len(t, d, 1, "got %v", d)
	var entry map[string]string
	_ = json.Unmarshal(d["name"], &entry)
	require.Equal(t, "Alice", entry["before"], "got %v", entry)
	require.Equal(t, "Bob", entry["after"], "got %v", entry)
}

func TestReportsAnAddedKeyWithBeforeNull(t *testing.T) {
	before := obj(map[string]string{})
	after := obj(map[string]string{"name": `"Alice"`})
	d := DiffJSONObjects(before, after)
	var entry map[string]json.RawMessage
	_ = json.Unmarshal(d["name"], &entry)
	require.Equal(t, "null", string(entry["before"]), "got before=%s", entry["before"])
}

func TestReportsARemovedKeyWithAfterNull(t *testing.T) {
	before := obj(map[string]string{"name": `"Alice"`})
	after := obj(map[string]string{})
	d := DiffJSONObjects(before, after)
	var entry map[string]json.RawMessage
	_ = json.Unmarshal(d["name"], &entry)
	require.Equal(t, "null", string(entry["after"]), "got after=%s", entry["after"])
}

func TestUnchangedKeysAreOmittedFromTheDiff(t *testing.T) {
	before := obj(map[string]string{"name": `"Alice"`, "age": "30"})
	after := obj(map[string]string{"name": `"Alice"`, "age": "31"})
	d := DiffJSONObjects(before, after)
	require.Len(t, d, 1, "got %v", d)
	_, ok := d["age"]
	require.True(t, ok, "expected age to be in the diff")
	_, ok = d["name"]
	require.False(t, ok, "expected name to be omitted")
}
