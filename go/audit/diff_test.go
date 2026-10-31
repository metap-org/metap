package audit

import (
	"encoding/json"
	"testing"
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
	if d := DiffJSONObjects(before, after); len(d) != 0 {
		t.Fatalf("expected empty diff, got %v", d)
	}
}

func TestReportsAChangedKey(t *testing.T) {
	before := obj(map[string]string{"name": `"Alice"`})
	after := obj(map[string]string{"name": `"Bob"`})
	d := DiffJSONObjects(before, after)
	if len(d) != 1 {
		t.Fatalf("got %v", d)
	}
	var entry map[string]string
	_ = json.Unmarshal(d["name"], &entry)
	if entry["before"] != "Alice" || entry["after"] != "Bob" {
		t.Fatalf("got %v", entry)
	}
}

func TestReportsAnAddedKeyWithBeforeNull(t *testing.T) {
	before := obj(map[string]string{})
	after := obj(map[string]string{"name": `"Alice"`})
	d := DiffJSONObjects(before, after)
	var entry map[string]json.RawMessage
	_ = json.Unmarshal(d["name"], &entry)
	if string(entry["before"]) != "null" {
		t.Fatalf("got before=%s", entry["before"])
	}
}

func TestReportsARemovedKeyWithAfterNull(t *testing.T) {
	before := obj(map[string]string{"name": `"Alice"`})
	after := obj(map[string]string{})
	d := DiffJSONObjects(before, after)
	var entry map[string]json.RawMessage
	_ = json.Unmarshal(d["name"], &entry)
	if string(entry["after"]) != "null" {
		t.Fatalf("got after=%s", entry["after"])
	}
}

func TestUnchangedKeysAreOmittedFromTheDiff(t *testing.T) {
	before := obj(map[string]string{"name": `"Alice"`, "age": "30"})
	after := obj(map[string]string{"name": `"Alice"`, "age": "31"})
	d := DiffJSONObjects(before, after)
	if len(d) != 1 {
		t.Fatalf("got %v", d)
	}
	if _, ok := d["age"]; !ok {
		t.Fatal("expected age to be in the diff")
	}
	if _, ok := d["name"]; ok {
		t.Fatal("expected name to be omitted")
	}
}
