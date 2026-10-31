package reconciler

import (
	"strings"
	"testing"

	"github.com/metap-org/metap-go/metadata"
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
	if got := TableNameFor("hr.employees"); got != "hr_employees" {
		t.Fatalf("got %q", got)
	}
}

func TestQualifiedTableNameForDefaultsToEntitySchema(t *testing.T) {
	if got := QualifiedTableNameFor("hr.employees"); got != "entities.hr_employees" {
		t.Fatalf("got %q", got)
	}
	if QualifiedTableNameFor("hr.employees") != QualifiedTableNameIn("hr.employees", EntitySchema) {
		t.Fatal("expected equal")
	}
}

func TestQualifiedTableNameInHonorsADifferentSchema(t *testing.T) {
	if got := QualifiedTableNameIn("hr.employees", "t_acme"); got != "t_acme.hr_employees" {
		t.Fatalf("got %q", got)
	}
}

func TestFrameworkColumnsAlwaysPresent(t *testing.T) {
	schema, err := Compile(testEntity("hr.departments", nil))
	if err != nil {
		t.Fatal(err)
	}
	if schema.Table != "entities.hr_departments" {
		t.Fatalf("got %q", schema.Table)
	}
	for _, name := range FrameworkColumnNames() {
		if _, ok := schema.Columns[name]; !ok {
			t.Fatalf("missing framework column %s", name)
		}
	}
}

func TestUnpromotedFieldGetsNoColumnAndNoIndex(t *testing.T) {
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{plainField("notes", metadata.KindString)}))
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Columns) != len(frameworkColumns) {
		t.Fatalf("got %d columns", len(schema.Columns))
	}
	if len(schema.Indexes) != 0 {
		t.Fatalf("expected no indexes, got %d", len(schema.Indexes))
	}
}

func TestIndexedFieldGetsExpressionIndexNotAColumn(t *testing.T) {
	f := plainField("departmentId", metadata.KindString)
	f.Indexed = boolp(true)
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Columns) != len(frameworkColumns) {
		t.Fatal("no new column expected")
	}
	if len(schema.Indexes) != 1 {
		t.Fatalf("got %d indexes", len(schema.Indexes))
	}
	for _, idx := range schema.Indexes {
		if idx.Expression != "((data ->> 'departmentId')::text)" {
			t.Fatalf("got %q", idx.Expression)
		}
		if idx.Unique {
			t.Fatal("expected not unique")
		}
	}
}

func TestIndexedDateFieldGetsAnUncastTextExpressionIndex(t *testing.T) {
	f := plainField("dueDate", metadata.KindDate)
	f.Indexed = boolp(true)
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Indexes) != 1 {
		t.Fatalf("got %d", len(schema.Indexes))
	}
	for _, idx := range schema.Indexes {
		if idx.Expression != "(data ->> 'dueDate')" {
			t.Fatalf("got %q", idx.Expression)
		}
	}
}

func TestStorageColumnOverridePromotesToARealColumn(t *testing.T) {
	f := plainField("amount", metadata.KindMoney)
	f.Indexed = boolp(true)
	storage := metadata.StorageColumn
	f.Storage = &storage
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	if err != nil {
		t.Fatal(err)
	}
	col, ok := schema.Columns["amount"]
	if !ok {
		t.Fatal("real column expected")
	}
	if col.SQLType != "numeric(18,4)" {
		t.Fatalf("got %q", col.SQLType)
	}
	if col.Origin.Kind != OriginGenerated {
		t.Fatal("expected Generated origin")
	}
	if len(schema.Indexes) != 1 {
		t.Fatalf("got %d", len(schema.Indexes))
	}
}

func TestUniqueStorageColumnGetsASinglePartialUniqueIndexNotABlanketConstraint(t *testing.T) {
	f := plainField("sku", metadata.KindString)
	f.Unique = boolp(true)
	storage := metadata.StorageColumn
	f.Storage = &storage
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Uniques) != 0 {
		t.Fatal("expected no blanket unique constraints")
	}
	if len(schema.Indexes) != 1 {
		t.Fatalf("got %d", len(schema.Indexes))
	}
	for _, idx := range schema.Indexes {
		if !idx.Unique {
			t.Fatal("expected unique")
		}
		if idx.WhereClause != "deleted = false" {
			t.Fatalf("got %q", idx.WhereClause)
		}
	}
}

func TestReferenceFieldGetsFkRegardlessOfIndexedFlag(t *testing.T) {
	f := plainField("departmentId", metadata.KindReference)
	f.RefEntity = strp("hr.departments")
	schema, err := Compile(testEntity("hr.employees", []metadata.EntityField{f}))
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.ForeignKeys) != 1 {
		t.Fatalf("got %d", len(schema.ForeignKeys))
	}
	for _, fk := range schema.ForeignKeys {
		if fk.RefTable != "entities.hr_departments" {
			t.Fatalf("got %q", fk.RefTable)
		}
		if fk.RefColumn != "id" {
			t.Fatalf("got %q", fk.RefColumn)
		}
	}
	if len(schema.Indexes) != 1 {
		t.Fatalf("got %d", len(schema.Indexes))
	}
	for _, idx := range schema.Indexes {
		if idx.Expression != `"departmentId"` {
			t.Fatalf("got %q", idx.Expression)
		}
	}
}

func TestSearchableFtsFieldGetsGinTsvectorExpressionIndex(t *testing.T) {
	f := plainField("description", metadata.KindString)
	f.Searchable = boolp(true)
	f.SearchMode = strp("fts")
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	if err != nil {
		t.Fatal(err)
	}
	for _, idx := range schema.Indexes {
		if !strings.Contains(idx.Expression, "to_tsvector") {
			t.Fatalf("got %q", idx.Expression)
		}
		if idx.Using != "gin" {
			t.Fatalf("got %q", idx.Using)
		}
	}
}

func TestSearchableSubstringFieldGetsGinTrgmExpressionIndex(t *testing.T) {
	f := plainField("title", metadata.KindString)
	f.Searchable = boolp(true)
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	if err != nil {
		t.Fatal(err)
	}
	for _, idx := range schema.Indexes {
		if !strings.Contains(idx.Expression, "gin_trgm_ops") {
			t.Fatalf("got %q", idx.Expression)
		}
	}
}

func TestSearchableAndUniqueFieldGetsBothAGinIndexAndAPartialUniqueIndex(t *testing.T) {
	f := plainField("hostname", metadata.KindString)
	f.Searchable = boolp(true)
	f.Unique = boolp(true)
	schema, err := Compile(testEntity("t.e", []metadata.EntityField{f}))
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Indexes) != 2 {
		t.Fatalf("expected 2 indexes, got %d", len(schema.Indexes))
	}
	var foundGin, foundUnique bool
	for _, idx := range schema.Indexes {
		if strings.Contains(idx.Expression, "gin_trgm_ops") {
			foundGin = true
			if idx.Unique {
				t.Fatal("trgm index must not be unique")
			}
		}
		if idx.Unique {
			foundUnique = true
			if idx.WhereClause != "deleted = false" {
				t.Fatalf("got %q", idx.WhereClause)
			}
		}
	}
	if !foundGin || !foundUnique {
		t.Fatal("expected both a trgm index and a unique index")
	}
}

func TestCompositeUniqueConstraintBuildsOnePartialUniqueIndexOverBothFields(t *testing.T) {
	e := testEntity("t.e", []metadata.EntityField{plainField("type", metadata.KindString), plainField("value", metadata.KindString)})
	e.UniqueConstraints = []metadata.EntityUniqueConstraint{{Fields: []string{"type", "value"}}}
	schema, err := Compile(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Indexes) != 1 {
		t.Fatalf("expected exactly 1 index, got %d", len(schema.Indexes))
	}
	for _, idx := range schema.Indexes {
		if !idx.Unique {
			t.Fatal("expected unique")
		}
		if idx.WhereClause != "deleted = false" {
			t.Fatalf("got %q", idx.WhereClause)
		}
		if !strings.Contains(idx.Expression, "'type'") || !strings.Contains(idx.Expression, "'value'") {
			t.Fatalf("got %q", idx.Expression)
		}
	}
}

func TestCompositeUniqueIndexNameIsDeterministicallyTruncatedWhenTooLong(t *testing.T) {
	fields := []string{strings.Repeat("a", 30), strings.Repeat("b", 30), strings.Repeat("c", 30)}
	name := compositeUniqueIndexName("t.e", fields)
	if len(name) > 63 {
		t.Fatalf("got length %d", len(name))
	}
	if name != compositeUniqueIndexName("t.e", fields) {
		t.Fatal("expected stable across calls")
	}
}

func TestFieldNameCollidingWithAFrameworkColumnIsRejected(t *testing.T) {
	_, err := Compile(testEntity("t.e", []metadata.EntityField{plainField("tenant_id", metadata.KindString)}))
	if err == nil || !strings.Contains(err.Error(), "tenant_id") {
		t.Fatalf("got %v", err)
	}
}

func TestShortEntityNamePassesTheTableNameLengthGuard(t *testing.T) {
	if err := CheckTableNameLength("hr.employees"); err != nil {
		t.Fatal(err)
	}
}

func TestEntityNameManglingToOver63BytesIsRejectedNotSilentlyTruncated(t *testing.T) {
	longName := "test." + strings.Repeat("a", 70)
	err := CheckTableNameLength(longName)
	if err == nil || !strings.Contains(err.Error(), "63-byte") {
		t.Fatalf("got %v", err)
	}
}

func TestCompileRejectsAnEntityWhoseTableNameWouldBeTooLong(t *testing.T) {
	longName := "test." + strings.Repeat("a", 70)
	_, err := Compile(testEntity(longName, nil))
	if err == nil || !strings.Contains(err.Error(), "63-byte") {
		t.Fatalf("got %v", err)
	}
}
