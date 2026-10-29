package metadata

import (
	"encoding/json"
	"testing"
)

func boolPtr(b bool) *bool                    { return &b }
func strPtr(s string) *string                 { return &s }
func storagePtr(s FieldStorage) *FieldStorage { return &s }

func plainField(kind FieldKind) EntityField {
	return EntityField{Name: "f", Label: "F", Kind: kind}
}

func TestNoFlagsAndNoOverrideStaysJsonb(t *testing.T) {
	if ResolveFieldStorageTier(plainField(KindString)) != TierJsonb {
		t.Fatal("expected Jsonb")
	}
}

func TestIndexedPromotesToGeneratedColumn(t *testing.T) {
	f := plainField(KindString)
	f.Indexed = boolPtr(true)
	if ResolveFieldStorageTier(f) != TierGeneratedColumn {
		t.Fatal("expected GeneratedColumn")
	}
}

func TestSortablePromotesToGeneratedColumn(t *testing.T) {
	f := plainField(KindDatetime)
	f.Sortable = boolPtr(true)
	if ResolveFieldStorageTier(f) != TierGeneratedColumn {
		t.Fatal("expected GeneratedColumn")
	}
}

func TestUniquePromotesToGeneratedColumn(t *testing.T) {
	f := plainField(KindString)
	f.Unique = boolPtr(true)
	if ResolveFieldStorageTier(f) != TierGeneratedColumn {
		t.Fatal("expected GeneratedColumn")
	}
}

func TestSearchablePromotesToGeneratedColumn(t *testing.T) {
	f := plainField(KindString)
	f.Searchable = boolPtr(true)
	if ResolveFieldStorageTier(f) != TierGeneratedColumn {
		t.Fatal("expected GeneratedColumn")
	}
}

func TestNativeOverrideForcesJsonbEvenWhenIndexed(t *testing.T) {
	f := plainField(KindString)
	f.Indexed = boolPtr(true)
	f.Storage = storagePtr(StorageNative)
	if ResolveFieldStorageTier(f) != TierJsonb {
		t.Fatal("expected Jsonb")
	}
}

func TestColumnOverrideForcesPromotionWithNoFlagsSet(t *testing.T) {
	f := plainField(KindString)
	f.Storage = storagePtr(StorageColumn)
	if ResolveFieldStorageTier(f) != TierGeneratedColumn {
		t.Fatal("expected GeneratedColumn")
	}
}

func TestFieldKindSqlTypeMappingMatchesTheDesignDocTable(t *testing.T) {
	cases := map[FieldKind]string{
		KindString: "text", KindEnum: "text", KindNumber: "double precision", KindMoney: "numeric(18,4)",
		KindBoolean: "boolean", KindDate: "date", KindDatetime: "timestamptz",
		KindReference: "uuid", KindID: "uuid", KindJSON: "jsonb",
	}
	for kind, want := range cases {
		if got := FieldKindSQLType(kind); got != want {
			t.Fatalf("%s: got %q, want %q", kind, got, want)
		}
	}
}

func TestStorageFieldRoundTripsThroughJSONCamelCase(t *testing.T) {
	f := plainField(KindString)
	f.Storage = storagePtr(StorageColumn)
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["storage"] != "column" {
		t.Fatalf("got %v", m["storage"])
	}
	var back EntityField
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Storage == nil || *back.Storage != StorageColumn {
		t.Fatalf("got %v", back.Storage)
	}
}

func TestStorageOmittedFromJSONWhenNone(t *testing.T) {
	b, _ := json.Marshal(plainField(KindString))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["storage"]; ok {
		t.Fatal("expected storage to be omitted")
	}
}

func TestMissingStorageKeyDeserializesToNoneForOldLowCodeDefinitions(t *testing.T) {
	raw := []byte(`{"name":"f","label":"F","kind":"string"}`)
	var f EntityField
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Storage != nil {
		t.Fatal("expected nil storage")
	}
}

func TestEnumTonesRoundTripsThroughJSONCamelCaseWithoutResolveVia(t *testing.T) {
	hint := FieldDisplayHint{Field: "status", EnumTones: map[string]string{"failed": "destructive"}}
	b, _ := json.Marshal(hint)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["resolveVia"]; ok {
		t.Fatal("expected resolveVia omitted")
	}
	tones := m["enumTones"].(map[string]any)
	if tones["failed"] != "destructive" {
		t.Fatalf("got %v", tones)
	}
	var back FieldDisplayHint
	_ = json.Unmarshal(b, &back)
	if back.EnumTones["failed"] != "destructive" || back.ResolveVia != nil {
		t.Fatalf("got %+v", back)
	}
}

func TestOnlyFieldDeserializesWithBothHintsNoneForOldDefinitions(t *testing.T) {
	raw := []byte(`{"field":"assignedTo"}`)
	var hint FieldDisplayHint
	if err := json.Unmarshal(raw, &hint); err != nil {
		t.Fatal(err)
	}
	if hint.ResolveVia != nil || hint.EnumTones != nil {
		t.Fatalf("got %+v", hint)
	}
}
