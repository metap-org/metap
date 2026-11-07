package reconciler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/stretchr/testify/require"
)

func plainField(name string, kind metadata.FieldKind) metadata.EntityField {
	return metadata.EntityField{Name: name, Label: name, Kind: kind}
}

func testEntity(name string, fields []metadata.EntityField) metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: name, Label: name, TableName: QualifiedTableNameFor(name),
		Fields:    fields,
		ListViews: []metadata.EntityListView{{Name: "default", Label: "Default", MaxLimit: 50}},
	}
}

func boolp(b bool) *bool    { return &b }
func strp(s string) *string { return &s }

func TestTableNameManglesDots(t *testing.T) {
	got := TableNameFor("hr.employees")
	require.Equal(t, "hr_employees", got, "got %q", got)
}

func TestQualifiedTableNameForDefaultsToEntitySchema(t *testing.T) {
	got := QualifiedTableNameFor("hr.employees")
	require.Equal(t, "entities.hr_employees", got, "got %q", got)
	require.Equal(t, QualifiedTableNameIn("hr.employees", EntitySchema), QualifiedTableNameFor("hr.employees"), "expected equal")
}

func TestQualifiedTableNameInHonorsADifferentSchema(t *testing.T) {
	got := QualifiedTableNameIn("hr.employees", "t_acme")
	require.Equal(t, "t_acme.hr_employees", got, "got %q", got)
}

func TestFrameworkColumnsAlwaysPresent(t *testing.T) {
	schema, err := Compile(testEntity("hr.departments", nil))
	require.NoError(t, err)
	require.Equal(t, "entities.hr_departments", schema.Table, "got %q", schema.Table)
	for _, name := range FrameworkColumnNames() {
		_, ok := schema.Columns[name]
		require.True(t, ok, "missing framework column %s", name)
	}
}

func TestUnpromotedFieldGetsNoColumnAndNoIndex(t *testing.T) {
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{plainField("notes", metadata.KindString)}))
	require.NoError(t, err)
	require.Len(t, schema.Columns, len(frameworkColumns), "got %d columns", len(schema.Columns))
	if len(fieldIdx(schema)) != 0 {
		require.FailNow(t, fmt.Sprintf("expected no indexes, got %d", len(fieldIdx(schema))))
	}
}

func TestIndexedFieldGetsExpressionIndexNotAColumn(t *testing.T) {
	f := plainField("departmentId", metadata.KindString)
	f.Indexed = boolp(true)
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	require.NoError(t, err)
	require.Len(t, schema.Columns, len(frameworkColumns), "no new column expected")
	if len(fieldIdx(schema)) != 1 {
		require.FailNow(t, fmt.Sprintf("got %d indexes", len(fieldIdx(schema))))
	}
	for _, idx := range fieldIdx(schema) {
		require.Equal(t, `"tenant_id", ((data ->> 'departmentId')::text)`, idx.Expression, "got %q", idx.Expression)
		require.False(t, idx.Unique, "expected not unique")
	}
}

func TestIndexedDateFieldGetsAnUncastTextExpressionIndex(t *testing.T) {
	f := plainField("dueDate", metadata.KindDate)
	f.Indexed = boolp(true)
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	require.NoError(t, err)
	if len(fieldIdx(schema)) != 1 {
		require.FailNow(t, fmt.Sprintf("got %d", len(fieldIdx(schema))))
	}
	for _, idx := range fieldIdx(schema) {
		require.Equal(t, `"tenant_id", (data ->> 'dueDate')`, idx.Expression, "got %q", idx.Expression)
	}
}

func TestStorageColumnOverridePromotesToARealColumn(t *testing.T) {
	f := plainField("amount", metadata.KindMoney)
	f.Indexed = boolp(true)
	storage := metadata.StorageColumn
	f.Storage = &storage
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	require.NoError(t, err)
	col, ok := schema.Columns["amount"]
	require.True(t, ok, "real column expected")
	require.Equal(t, "numeric(18,4)", col.SQLType, "got %q", col.SQLType)
	require.Equal(t, OriginGenerated, col.Origin.Kind, "expected Generated origin")
	if len(fieldIdx(schema)) != 1 {
		require.FailNow(t, fmt.Sprintf("got %d", len(fieldIdx(schema))))
	}
}

func TestUniqueStorageColumnGetsASinglePartialUniqueIndexNotABlanketConstraint(t *testing.T) {
	f := plainField("sku", metadata.KindString)
	f.Unique = boolp(true)
	storage := metadata.StorageColumn
	f.Storage = &storage
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	require.NoError(t, err)
	require.Len(t, schema.Uniques, 0, "expected no blanket unique constraints")
	if len(fieldIdx(schema)) != 1 {
		require.FailNow(t, fmt.Sprintf("got %d", len(fieldIdx(schema))))
	}
	for _, idx := range fieldIdx(schema) {
		require.True(t, idx.Unique, "expected unique")
		require.Equal(t, "deleted = false", idx.WhereClause, "got %q", idx.WhereClause)
	}
}

func TestReferenceFieldGetsFkRegardlessOfIndexedFlag(t *testing.T) {
	f := plainField("departmentId", metadata.KindReference)
	f.RefEntity = strp("hr.departments")
	schema, err := Compile(testEntity("hr.employees", []metadata.EntityField{f}))
	require.NoError(t, err)
	require.Len(t, schema.ForeignKeys, 1, "got %d", len(schema.ForeignKeys))
	for _, fk := range schema.ForeignKeys {
		require.Equal(t, "entities.hr_departments", fk.RefTable, "got %q", fk.RefTable)
		require.Equal(t, "id", fk.RefColumn, "got %q", fk.RefColumn)
	}
	if len(fieldIdx(schema)) != 1 {
		require.FailNow(t, fmt.Sprintf("got %d", len(fieldIdx(schema))))
	}
	for _, idx := range fieldIdx(schema) {
		require.Equal(t, `"departmentId"`, idx.Expression, "got %q", idx.Expression)
	}
}

func TestSearchableFtsFieldGetsGinTsvectorExpressionIndex(t *testing.T) {
	f := plainField("description", metadata.KindString)
	f.Searchable = boolp(true)
	f.SearchMode = strp("fts")
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	require.NoError(t, err)
	for _, idx := range fieldIdx(schema) {
		require.Contains(t, idx.Expression, "to_tsvector", "got %q", idx.Expression)
		require.Equal(t, "gin", idx.Using, "got %q", idx.Using)
	}
}

func TestSearchableSubstringFieldGetsGinTrgmExpressionIndex(t *testing.T) {
	f := plainField("title", metadata.KindString)
	f.Searchable = boolp(true)
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	require.NoError(t, err)
	for _, idx := range fieldIdx(schema) {
		require.Contains(t, idx.Expression, "gin_trgm_ops", "got %q", idx.Expression)
	}
}

func TestSearchableAndUniqueFieldGetsBothAGinIndexAndAPartialUniqueIndex(t *testing.T) {
	f := plainField("hostname", metadata.KindString)
	f.Searchable = boolp(true)
	f.Unique = boolp(true)
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	require.NoError(t, err)
	if len(fieldIdx(schema)) != 2 {
		require.FailNow(t, fmt.Sprintf("expected 2 indexes, got %d", len(fieldIdx(schema))))
	}
	var foundGin, foundUnique bool
	for _, idx := range fieldIdx(schema) {
		if strings.Contains(idx.Expression, "gin_trgm_ops") {
			foundGin = true
			require.False(t, idx.Unique, "trgm index must not be unique")
		}
		if idx.Unique {
			foundUnique = true
			require.Equal(t, "deleted = false", idx.WhereClause, "got %q", idx.WhereClause)
		}
	}
	require.True(t, foundGin, "expected both a trgm index and a unique index")
	require.True(t, foundUnique, "expected both a trgm index and a unique index")
}

func TestCompositeUniqueConstraintBuildsOnePartialUniqueIndexOverBothFields(t *testing.T) {
	e := testEntity("t.e", []metadata.EntityField{plainField("type", metadata.KindString), plainField("value", metadata.KindString)})
	e.UniqueConstraints = []metadata.EntityUniqueConstraint{{Fields: []string{"type", "value"}}}
	schema, err := Compile(e)
	require.NoError(t, err)
	if len(fieldIdx(schema)) != 1 {
		require.FailNow(t, fmt.Sprintf("expected exactly 1 index, got %d", len(fieldIdx(schema))))
	}
	for _, idx := range fieldIdx(schema) {
		require.True(t, idx.Unique, "expected unique")
		require.Equal(t, "deleted = false", idx.WhereClause, "got %q", idx.WhereClause)
		require.Contains(t, idx.Expression, "'type'", "got %q", idx.Expression)
		require.Contains(t, idx.Expression, "'value'", "got %q", idx.Expression)
	}
}

func TestCompositeUniqueIndexNameIsDeterministicallyTruncatedWhenTooLong(t *testing.T) {
	fields := []string{strings.Repeat("a", 30), strings.Repeat("b", 30), strings.Repeat("c", 30)}
	name := compositeUniqueIndexName("t.e", fields)
	require.False(t, len(name) > 63, "got length %d", len(name))
	require.Equal(t, compositeUniqueIndexName("t.e", fields), name, "expected stable across calls")
}

func TestFieldNameCollidingWithAFrameworkColumnIsRejected(t *testing.T) {
	_, err := Compile(testEntity("t.e", []metadata.EntityField{plainField("tenant_id", metadata.KindString)}))
	require.Error(t, err, "got %v", err)
	require.Contains(t, err.Error(), "tenant_id", "got %v", err)
}

func TestShortEntityNamePassesTheTableNameLengthGuard(t *testing.T) {
	require.NoError(t, CheckTableNameLength("hr.employees"))
}

func TestEntityNameManglingToOver63BytesIsRejectedNotSilentlyTruncated(t *testing.T) {
	longName := "test." + strings.Repeat("a", 70)
	err := CheckTableNameLength(longName)
	require.Error(t, err, "got %v", err)
	require.Contains(t, err.Error(), "63-byte", "got %v", err)
}

func TestCompileRejectsAnEntityWhoseTableNameWouldBeTooLong(t *testing.T) {
	longName := "test." + strings.Repeat("a", 70)
	_, err := Compile(testEntity(longName, nil))
	require.Error(t, err, "got %v", err)
	require.Contains(t, err.Error(), "63-byte", "got %v", err)
}

// fieldIdx is the schema's indexes minus the tenant_id index every table gets, so per-field
// tests can count only the indexes their field produced.
func fieldIdx(schema PhysicalSchema) map[string]IndexSpec {
	out := map[string]IndexSpec{}
	for name, idx := range schema.Indexes {
		if strings.HasPrefix(name, "idx_") && strings.HasSuffix(name, "_tenant_id") {
			continue
		}
		out[name] = idx
	}
	return out
}

func TestEveryTableGetsATenantIDIndex(t *testing.T) {
	schema, err := Compile(testEntity("t.e", nil))
	require.NoError(t, err)
	idx, ok := schema.Indexes[indexName("t.e", "tenant_id", "idx")]
	require.True(t, ok, "got %+v (present=%v)", idx, ok)
	require.Equal(t, `"tenant_id"`, idx.Expression, "got %+v (present=%v)", idx, ok)
	require.False(t, idx.Unique, "got %+v (present=%v)", idx, ok)
}

func TestUniqueScopeTenantByDefaultGlobalWhenAsked(t *testing.T) {
	tenantF := plainField("slug", metadata.KindString)
	tenantF.Unique = boolp(true)
	globalF := plainField("hostname", metadata.KindString)
	globalF.Unique = boolp(true)
	g := metadata.UniqueScopeGlobal
	globalF.UniqueScope = &g
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{tenantF, globalF}))
	require.NoError(t, err)
	got := schema.Indexes[indexName("t.e", "slug", "uniq")].Expression
	require.Equal(t, `"tenant_id", ((data ->> 'slug')::text)`, got, "tenant-scoped unique: got %q", got)
	got = schema.Indexes[indexName("t.e", "hostname", "uniq")].Expression
	require.Equal(t, `((data ->> 'hostname')::text)`, got, "global unique: got %q", got)
}

func TestCompositeUniqueConstraintLeadsWithTenantUnlessGlobal(t *testing.T) {
	e := testEntity("t.e", []metadata.EntityField{plainField("type", metadata.KindString), plainField("value", metadata.KindString)})
	e.UniqueConstraints = []metadata.EntityUniqueConstraint{{Fields: []string{"type", "value"}}}
	schema, err := Compile(e)
	require.NoError(t, err)
	name := compositeUniqueIndexName("t.e", []string{"type", "value"})
	got := schema.Indexes[name].Expression
	require.True(t, strings.HasPrefix(got, `"tenant_id", `), "got %q", got)
	g := metadata.UniqueScopeGlobal
	e.UniqueConstraints[0].Scope = &g
	schema, _ = Compile(e)
	got = schema.Indexes[name].Expression
	require.NotContains(t, got, "tenant_id", "got %q", got)
}

func TestNonUniqueFkColumnKeepsSingleColumnIndex(t *testing.T) {
	f := plainField("projectId", metadata.KindReference)
	ref := "t.p"
	f.RefEntity = &ref
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	require.NoError(t, err)
	got := schema.Indexes[indexName("t.e", "projectId", "idx")].Expression
	require.Equal(t, `"projectId"`, got, "got %q", got)
}
