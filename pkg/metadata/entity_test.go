package metadata

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func boolPtr(b bool) *bool                    { return &b }
func strPtr(s string) *string                 { return &s }
func storagePtr(s FieldStorage) *FieldStorage { return &s }

func plainField(kind FieldKind) EntityField {
	return EntityField{Name: "f", Label: "F", Kind: kind}
}

func TestNoFlagsAndNoOverrideStaysJsonb(t *testing.T) {
	require.Equal(t, TierJsonb, ResolveFieldStorageTier(plainField(KindString)), "expected Jsonb")
}

func TestIndexedPromotesToGeneratedColumn(t *testing.T) {
	f := plainField(KindString)
	f.Indexed = boolPtr(true)
	require.Equal(t, TierGeneratedColumn, ResolveFieldStorageTier(f), "expected GeneratedColumn")
}

func TestSortablePromotesToGeneratedColumn(t *testing.T) {
	f := plainField(KindDatetime)
	f.Sortable = boolPtr(true)
	require.Equal(t, TierGeneratedColumn, ResolveFieldStorageTier(f), "expected GeneratedColumn")
}

func TestUniquePromotesToGeneratedColumn(t *testing.T) {
	f := plainField(KindString)
	f.Unique = boolPtr(true)
	require.Equal(t, TierGeneratedColumn, ResolveFieldStorageTier(f), "expected GeneratedColumn")
}

func TestSearchablePromotesToGeneratedColumn(t *testing.T) {
	f := plainField(KindString)
	f.Searchable = boolPtr(true)
	require.Equal(t, TierGeneratedColumn, ResolveFieldStorageTier(f), "expected GeneratedColumn")
}

func TestNativeOverrideForcesJsonbEvenWhenIndexed(t *testing.T) {
	f := plainField(KindString)
	f.Indexed = boolPtr(true)
	f.Storage = storagePtr(StorageNative)
	require.Equal(t, TierJsonb, ResolveFieldStorageTier(f), "expected Jsonb")
}

func TestColumnOverrideForcesPromotionWithNoFlagsSet(t *testing.T) {
	f := plainField(KindString)
	f.Storage = storagePtr(StorageColumn)
	require.Equal(t, TierGeneratedColumn, ResolveFieldStorageTier(f), "expected GeneratedColumn")
}

func TestFieldKindSqlTypeMappingMatchesTheDesignDocTable(t *testing.T) {
	cases := map[FieldKind]string{
		KindString: "text", KindEnum: "text", KindNumber: "double precision", KindMoney: "numeric(18,4)",
		KindBoolean: "boolean", KindDate: "date", KindDatetime: "timestamptz",
		KindReference: "uuid", KindID: "uuid", KindJSON: "jsonb",
	}
	for kind, want := range cases {
		got := FieldKindSQLType(kind)
		require.Equal(t, want, got, "%s: got %q, want %q", kind, got, want)
	}
}

func TestStorageFieldRoundTripsThroughJSONCamelCase(t *testing.T) {
	f := plainField(KindString)
	f.Storage = storagePtr(StorageColumn)
	b, err := json.Marshal(f)
	require.NoError(t, err)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	require.Equal(t, "column", m["storage"], "got %v", m["storage"])
	var back EntityField
	require.NoError(t, json.Unmarshal(b, &back))
	require.NotNil(t, back.Storage, "got %v", back.Storage)
	require.Equal(t, StorageColumn, *back.Storage, "got %v", back.Storage)
}

func TestStorageOmittedFromJSONWhenNone(t *testing.T) {
	b, _ := json.Marshal(plainField(KindString))
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	_, ok := m["storage"]
	require.False(t, ok, "expected storage to be omitted")
}

func TestMissingStorageKeyDeserializesToNoneForOldLowCodeDefinitions(t *testing.T) {
	raw := []byte(`{"name":"f","label":"F","kind":"string"}`)
	var f EntityField
	require.NoError(t, json.Unmarshal(raw, &f))
	require.Nil(t, f.Storage, "expected nil storage")
}

func TestEnumTonesRoundTripsThroughJSONCamelCaseWithoutResolveVia(t *testing.T) {
	hint := FieldDisplayHint{Field: "status", EnumTones: map[string]string{"failed": "destructive"}}
	b, _ := json.Marshal(hint)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	_, ok := m["resolveVia"]
	require.False(t, ok, "expected resolveVia omitted")
	tones := m["enumTones"].(map[string]any)
	require.Equal(t, "destructive", tones["failed"], "got %v", tones)
	var back FieldDisplayHint
	_ = json.Unmarshal(b, &back)
	require.Equal(t, "destructive", back.EnumTones["failed"], "got %+v", back)
	require.Nil(t, back.ResolveVia, "got %+v", back)
}

func TestOnlyFieldDeserializesWithBothHintsNoneForOldDefinitions(t *testing.T) {
	raw := []byte(`{"field":"assignedTo"}`)
	var hint FieldDisplayHint
	require.NoError(t, json.Unmarshal(raw, &hint))
	require.Nil(t, hint.ResolveVia, "got %+v", hint)
	require.Nil(t, hint.EnumTones, "got %+v", hint)
}
