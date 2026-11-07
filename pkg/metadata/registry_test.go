package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
	require.NoError(t, registry.Register(regEntity("crm.customers", []EntityField{regField("name", KindString)})))
	_, ok := registry.GetEntity("crm.customers")
	require.True(t, ok, "expected to find crm.customers")
	_, ok = registry.GetEntity("crm.unknown")
	require.False(t, ok, "expected not to find crm.unknown")
}

func TestDuplicateRegistrationIsRejected(t *testing.T) {
	registry := NewMetadataRegistry()
	require.NoError(t, registry.Register(regEntity("crm.customers", nil)))
	err := registry.Register(regEntity("crm.customers", nil))
	re, ok := err.(*RegistryError)
	require.True(t, ok, "expected AlreadyRegistered error, got %v", err)
	require.NotEqual(t, "", re.AlreadyRegistered, "expected AlreadyRegistered error, got %v", err)
}

func TestValidateReferencesRejectsUnknownRefEntity(t *testing.T) {
	registry := NewMetadataRegistry()
	field := regField("parent", KindReference)
	field.RefEntity = strPtr("crm.ghost")
	require.NoError(t, registry.Register(regEntity("crm.customers", []EntityField{field})))
	err := registry.ValidateReferences()
	ve, ok := err.(*ValidationError)
	require.True(t, ok, "expected ValidationError, got %v", err)
	found := false
	for _, issue := range ve.Issues {
		if containsSub(issue, `unknown entity "crm.ghost"`) {
			found = true
		}
	}
	require.True(t, found, "got %v", ve.Issues)
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
	require.NoError(t, registry.Register(regEntity("crm.vendors", []EntityField{regField("name", KindString)})))
	field := regField("vendor", KindReference)
	field.RefEntity = strPtr("crm.vendors")
	field.RefDisplayField = strPtr("name")
	require.NoError(t, registry.Register(regEntity("crm.customers", []EntityField{field})))
	require.NoError(t, registry.ValidateReferences(), "unexpected error")
}

func TestValidateReferencesRejectsUnknownRefDisplayField(t *testing.T) {
	registry := NewMetadataRegistry()
	require.NoError(t, registry.Register(regEntity("test.owners", []EntityField{regField("nickname_typo", KindString)})))
	field := regField("owner_id", KindReference)
	field.RefEntity = strPtr("test.owners")
	field.RefDisplayField = strPtr("nickname")
	require.NoError(t, registry.Register(regEntity("test.widgets", []EntityField{field})))
	err := registry.ValidateReferences()
	ve, ok := err.(*ValidationError)
	require.True(t, ok, "expected ValidationError, got %v", err)
	found := false
	for _, issue := range ve.Issues {
		if containsSub(issue, `refDisplayField "nickname"`) {
			found = true
		}
	}
	require.True(t, found, "got %v", ve.Issues)
}

func TestMergeWithCombinesEntitiesFromBothSources(t *testing.T) {
	base := NewMetadataRegistry()
	require.NoError(t, base.Register(regEntity("crm.customers", []EntityField{regField("name", KindString)})))
	merged, err := base.MergeWith([]EntityDefinition{regEntity("lowcode.demo", []EntityField{regField("title", KindString)})})
	require.NoError(t, err)
	_, ok := merged.GetEntity("crm.customers")
	require.True(t, ok, "expected crm.customers in merged")
	_, ok = merged.GetEntity("lowcode.demo")
	require.True(t, ok, "expected lowcode.demo in merged")
	_, ok = base.GetEntity("lowcode.demo")
	require.False(t, ok, "base registry must be untouched by the merge")
}

func TestMergeWithRejectsNameCollisionWithBase(t *testing.T) {
	base := NewMetadataRegistry()
	require.NoError(t, base.Register(regEntity("crm.customers", nil)))
	_, err := base.MergeWith([]EntityDefinition{regEntity("crm.customers", nil)})
	re, ok := err.(*RegistryError)
	require.True(t, ok, "expected AlreadyRegistered error, got %v", err)
	require.NotEqual(t, "", re.AlreadyRegistered, "expected AlreadyRegistered error, got %v", err)
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
	require.NoError(t, registry.RegisterAllSubmitted())
	_, ok := registry.GetEntity("test.submitted_a")
	require.True(t, ok, "expected test.submitted_a")
	_, ok = registry.GetEntity("test.submitted_b")
	require.True(t, ok, "expected test.submitted_b")
}

func TestRegisterAllSubmittedPicksUpRelatedViewsAlongsideEntities(t *testing.T) {
	registry := NewMetadataRegistry()
	require.NoError(t, registry.RegisterAllSubmitted())
	views := registry.GetRelatedViews("test.submitted_a")
	require.Len(t, views, 1, "got %v", views)
	require.Equal(t, "widgets", views[0].Name, "got %v", views)
	require.Equal(t, "test.widgets", views[0].Entity, "got %v", views)
}

func TestGetRelatedViewsIsEmptyForAnEntityWithNoneRegistered(t *testing.T) {
	registry := NewMetadataRegistry()
	require.NoError(t, registry.RegisterAllSubmitted())
	require.Len(t, registry.GetRelatedViews("test.submitted_b"), 0, "expected empty")
}

func TestRelatedViewsDoNotRequireTheTargetEntityToBeRegistered(t *testing.T) {
	registry := NewMetadataRegistry()
	require.NoError(t, registry.RegisterAllSubmitted())
	_, ok := registry.GetEntity("test.widgets")
	require.False(t, ok, "test.widgets must not be registered")
	require.Len(t, registry.GetRelatedViews("test.submitted_a"), 1, "expected 1 related view despite target not being registered")
}

func TestListEntitiesCarriesRelatedViewsIntoEntitySummary(t *testing.T) {
	registry := NewMetadataRegistry()
	require.NoError(t, registry.RegisterAllSubmitted())
	summary, ok := registry.GetEntityMetadata("test.submitted_a")
	require.True(t, ok, "expected summary")
	require.Len(t, summary.RelatedViews, 1, "got %v", summary.RelatedViews)
	require.Equal(t, "ownerId", summary.RelatedViews[0].FilterField, "got %v", summary.RelatedViews)
}

func TestRegisterAllSubmittedPicksUpFieldDisplayHintsAlongsideEntities(t *testing.T) {
	registry := NewMetadataRegistry()
	require.NoError(t, registry.RegisterAllSubmitted())
	hints := registry.GetFieldDisplayHints("test.submitted_a")
	require.Len(t, hints, 1, "got %v", hints)
	require.Equal(t, "assignedTo", hints[0].Field, "got %v", hints)
	require.NotNil(t, hints[0].ResolveVia, "got %v", hints)
	require.Equal(t, "users", *hints[0].ResolveVia, "got %v", hints)
}

func TestGetFieldDisplayHintsIsEmptyForAnEntityWithNoneRegistered(t *testing.T) {
	registry := NewMetadataRegistry()
	require.NoError(t, registry.RegisterAllSubmitted())
	require.Len(t, registry.GetFieldDisplayHints("test.submitted_b"), 0, "expected empty")
}

func TestListEntitiesCarriesFieldDisplayHintsIntoEntitySummary(t *testing.T) {
	registry := NewMetadataRegistry()
	require.NoError(t, registry.RegisterAllSubmitted())
	summary, ok := registry.GetEntityMetadata("test.submitted_a")
	require.True(t, ok, "expected summary")
	require.Len(t, summary.FieldDisplayHints, 1, "got %v", summary.FieldDisplayHints)
	require.NotNil(t, summary.FieldDisplayHints[0].ResolveVia, "got %v", summary.FieldDisplayHints)
	require.Equal(t, "users", *summary.FieldDisplayHints[0].ResolveVia, "got %v", summary.FieldDisplayHints)
}

func TestListEntitiesIncludesDeterministicVersionHash(t *testing.T) {
	registry := NewMetadataRegistry()
	require.NoError(t, registry.Register(regEntity("crm.customers", []EntityField{regField("name", KindString)})))
	summaries := registry.ListEntities()
	require.Len(t, summaries, 1, "got %d summaries", len(summaries))
	require.NotEqual(t, "", summaries[0].Version, "expected non-empty version")
}
