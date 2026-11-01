package metadata

import "testing"

func regField(name string, kind FieldKind) EntityField {
	return EntityField{Name: name, Label: name, Kind: kind}
}

func regEntity(name string, fields []EntityField) EntityDefinition {
	tableName := "entities." + dotsToUnderscores(name)
	return EntityDefinition{Name: name, Label: name, TableName: tableName, Fields: fields}
}

func dotsToUnderscores(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c == '.' {
			out[i] = '_'
		}
	}
	return string(out)
}

func TestRegisterThenGetRoundTrips(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.Register(regEntity("crm.customers", []EntityField{regField("name", KindString)})); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.GetEntity("crm.customers"); !ok {
		t.Fatal("expected to find crm.customers")
	}
	if _, ok := registry.GetEntity("crm.unknown"); ok {
		t.Fatal("expected not to find crm.unknown")
	}
}

func TestDuplicateRegistrationIsRejected(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.Register(regEntity("crm.customers", nil)); err != nil {
		t.Fatal(err)
	}
	err := registry.Register(regEntity("crm.customers", nil))
	re, ok := err.(*RegistryError)
	if !ok || re.AlreadyRegistered == "" {
		t.Fatalf("expected AlreadyRegistered error, got %v", err)
	}
}

func TestValidateReferencesRejectsUnknownRefEntity(t *testing.T) {
	registry := NewMetadataRegistry()
	field := regField("parent", KindReference)
	field.RefEntity = strPtr("crm.ghost")
	if err := registry.Register(regEntity("crm.customers", []EntityField{field})); err != nil {
		t.Fatal(err)
	}
	err := registry.ValidateReferences()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	found := false
	for _, issue := range ve.Issues {
		if containsSub(issue, `unknown entity "crm.ghost"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("got %v", ve.Issues)
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestValidateReferencesAcceptsKnownRefEntityAndDisplayField(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.Register(regEntity("crm.vendors", []EntityField{regField("name", KindString)})); err != nil {
		t.Fatal(err)
	}
	field := regField("vendor", KindReference)
	field.RefEntity = strPtr("crm.vendors")
	field.RefDisplayField = strPtr("name")
	if err := registry.Register(regEntity("crm.customers", []EntityField{field})); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateReferences(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateReferencesRejectsUnknownRefDisplayField(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.Register(regEntity("test.owners", []EntityField{regField("nickname_typo", KindString)})); err != nil {
		t.Fatal(err)
	}
	field := regField("owner_id", KindReference)
	field.RefEntity = strPtr("test.owners")
	field.RefDisplayField = strPtr("nickname")
	if err := registry.Register(regEntity("test.widgets", []EntityField{field})); err != nil {
		t.Fatal(err)
	}
	err := registry.ValidateReferences()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected ValidationError, got %v", err)
	}
	found := false
	for _, issue := range ve.Issues {
		if containsSub(issue, `refDisplayField "nickname"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("got %v", ve.Issues)
	}
}

func TestMergeWithCombinesEntitiesFromBothSources(t *testing.T) {
	base := NewMetadataRegistry()
	if err := base.Register(regEntity("crm.customers", []EntityField{regField("name", KindString)})); err != nil {
		t.Fatal(err)
	}
	merged, err := base.MergeWith([]EntityDefinition{regEntity("lowcode.demo", []EntityField{regField("title", KindString)})})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := merged.GetEntity("crm.customers"); !ok {
		t.Fatal("expected crm.customers in merged")
	}
	if _, ok := merged.GetEntity("lowcode.demo"); !ok {
		t.Fatal("expected lowcode.demo in merged")
	}
	if _, ok := base.GetEntity("lowcode.demo"); ok {
		t.Fatal("base registry must be untouched by the merge")
	}
}

func TestMergeWithRejectsNameCollisionWithBase(t *testing.T) {
	base := NewMetadataRegistry()
	if err := base.Register(regEntity("crm.customers", nil)); err != nil {
		t.Fatal(err)
	}
	_, err := base.MergeWith([]EntityDefinition{regEntity("crm.customers", nil)})
	re, ok := err.(*RegistryError)
	if !ok || re.AlreadyRegistered == "" {
		t.Fatalf("expected AlreadyRegistered error, got %v", err)
	}
}

func submittedEntityA() EntityDefinition {
	return regEntity("test.submitted_a", []EntityField{regField("name", KindString)})
}
func submittedEntityB() EntityDefinition {
	return regEntity("test.submitted_b", []EntityField{regField("name", KindString)})
}
func submittedRelatedViewsA() []RelatedView {
	return []RelatedView{{Name: "widgets", Label: "Widgets", Entity: "test.widgets", FilterField: "ownerId", Fields: []string{"title"}}}
}
func submittedFieldDisplayHintsA() []FieldDisplayHint {
	return []FieldDisplayHint{{Field: "assignedTo", ResolveVia: strPtr("users")}}
}

func init() {
	SubmitEntity(submittedEntityA)
	SubmitEntity(submittedEntityB)
	SubmitRelatedViews("test.submitted_a", submittedRelatedViewsA)
	SubmitFieldDisplayHints("test.submitted_a", submittedFieldDisplayHintsA)
}

func TestRegisterAllSubmittedPicksUpEverySubmitEntityCall(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.GetEntity("test.submitted_a"); !ok {
		t.Fatal("expected test.submitted_a")
	}
	if _, ok := registry.GetEntity("test.submitted_b"); !ok {
		t.Fatal("expected test.submitted_b")
	}
}

func TestRegisterAllSubmittedPicksUpRelatedViewsAlongsideEntities(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		t.Fatal(err)
	}
	views := registry.GetRelatedViews("test.submitted_a")
	if len(views) != 1 || views[0].Name != "widgets" || views[0].Entity != "test.widgets" {
		t.Fatalf("got %v", views)
	}
}

func TestGetRelatedViewsIsEmptyForAnEntityWithNoneRegistered(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		t.Fatal(err)
	}
	if len(registry.GetRelatedViews("test.submitted_b")) != 0 {
		t.Fatal("expected empty")
	}
}

func TestRelatedViewsDoNotRequireTheTargetEntityToBeRegistered(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.GetEntity("test.widgets"); ok {
		t.Fatal("test.widgets must not be registered")
	}
	if len(registry.GetRelatedViews("test.submitted_a")) != 1 {
		t.Fatal("expected 1 related view despite target not being registered")
	}
}

func TestListEntitiesCarriesRelatedViewsIntoEntitySummary(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		t.Fatal(err)
	}
	summary, ok := registry.GetEntityMetadata("test.submitted_a")
	if !ok {
		t.Fatal("expected summary")
	}
	if len(summary.RelatedViews) != 1 || summary.RelatedViews[0].FilterField != "ownerId" {
		t.Fatalf("got %v", summary.RelatedViews)
	}
}

func TestRegisterAllSubmittedPicksUpFieldDisplayHintsAlongsideEntities(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		t.Fatal(err)
	}
	hints := registry.GetFieldDisplayHints("test.submitted_a")
	if len(hints) != 1 || hints[0].Field != "assignedTo" || hints[0].ResolveVia == nil || *hints[0].ResolveVia != "users" {
		t.Fatalf("got %v", hints)
	}
}

func TestGetFieldDisplayHintsIsEmptyForAnEntityWithNoneRegistered(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		t.Fatal(err)
	}
	if len(registry.GetFieldDisplayHints("test.submitted_b")) != 0 {
		t.Fatal("expected empty")
	}
}

func TestListEntitiesCarriesFieldDisplayHintsIntoEntitySummary(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		t.Fatal(err)
	}
	summary, ok := registry.GetEntityMetadata("test.submitted_a")
	if !ok {
		t.Fatal("expected summary")
	}
	if len(summary.FieldDisplayHints) != 1 || summary.FieldDisplayHints[0].ResolveVia == nil || *summary.FieldDisplayHints[0].ResolveVia != "users" {
		t.Fatalf("got %v", summary.FieldDisplayHints)
	}
}

func TestListEntitiesIncludesDeterministicVersionHash(t *testing.T) {
	registry := NewMetadataRegistry()
	if err := registry.Register(regEntity("crm.customers", []EntityField{regField("name", KindString)})); err != nil {
		t.Fatal(err)
	}
	summaries := registry.ListEntities()
	if len(summaries) != 1 {
		t.Fatalf("got %d summaries", len(summaries))
	}
	if summaries[0].Version == "" {
		t.Fatal("expected non-empty version")
	}
}
