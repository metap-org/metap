package metadata

import (
	"fmt"
	"sync"
)

// EntitySummary mirrors the Rust EntitySummary — what GET /metadata/entities serves.
type EntitySummary struct {
	Name              string                   `json:"name"`
	Label             string                   `json:"label"`
	Fields            []EntityField            `json:"fields"`
	ListViews         []EntityListView         `json:"listViews"`
	Workflow          *EntityWorkflow          `json:"workflow,omitempty"`
	RelatedViews      []RelatedView            `json:"relatedViews,omitempty"`
	FieldDisplayHints []FieldDisplayHint       `json:"fieldDisplayHints,omitempty"`
	UniqueConstraints []EntityUniqueConstraint `json:"uniqueConstraints,omitempty"`
	Audit             *EntityAuditConfig       `json:"audit,omitempty"`
	Version           string                   `json:"version"`
}

// RegistryError mirrors the Rust RegistryError.
type RegistryError struct {
	AlreadyRegistered string // "" means this is a Validation error instead
	Validation        *ValidationError
}

func (e *RegistryError) Error() string {
	if e.AlreadyRegistered != "" {
		return fmt.Sprintf("Entity already registered: %s", e.AlreadyRegistered)
	}
	return e.Validation.Error()
}

func alreadyRegisteredErr(name string) *RegistryError   { return &RegistryError{AlreadyRegistered: name} }
func validationErr(err *ValidationError) *RegistryError { return &RegistryError{Validation: err} }

// --- Auto-discovery registration, the Go equivalent of submit_entity!/inventory ---
//
// Rust's `inventory` crate collects submissions via linker-section tricks that run before
// main(). Go's closest native equivalent is a package's own init() function, which the Go
// runtime guarantees runs (once, in import-dependency order) for every package transitively
// imported by the binary — the same "declared once, discovered automatically, no explicit call
// needed in main()" property submit_entity! provides. An entity-defining Go package calls
// metadata.SubmitEntity(factory) from its own init() instead of using a macro.
var submissionsMu sync.Mutex
var submittedEntityFactories []func() EntityDefinition
var submittedRelatedViews []relatedViewsSubmission
var submittedFieldDisplayHints []fieldDisplayHintsSubmission

type relatedViewsSubmission struct {
	entity  string
	factory func() []RelatedView
}
type fieldDisplayHintsSubmission struct {
	entity  string
	factory func() []FieldDisplayHint
}

// SubmitEntity registers factory for pickup by (*MetadataRegistry).RegisterAllSubmitted. Call
// from an init() function, next to the entity's own definition.
func SubmitEntity(factory func() EntityDefinition) {
	submissionsMu.Lock()
	defer submissionsMu.Unlock()
	submittedEntityFactories = append(submittedEntityFactories, factory)
}

// SubmitRelatedViews registers factory as the related-views section list for entity.
func SubmitRelatedViews(entity string, factory func() []RelatedView) {
	submissionsMu.Lock()
	defer submissionsMu.Unlock()
	submittedRelatedViews = append(submittedRelatedViews, relatedViewsSubmission{entity, factory})
}

// SubmitFieldDisplayHints registers factory as the field-display-hints list for entity.
func SubmitFieldDisplayHints(entity string, factory func() []FieldDisplayHint) {
	submissionsMu.Lock()
	defer submissionsMu.Unlock()
	submittedFieldDisplayHints = append(submittedFieldDisplayHints, fieldDisplayHintsSubmission{entity, factory})
}

// MetadataRegistry is a read-only-after-boot registry populated once by whichever binary
// registers entities.
type MetadataRegistry struct {
	entities          map[string]EntityDefinition
	relatedViews      map[string][]RelatedView
	fieldDisplayHints map[string][]FieldDisplayHint
}

func NewMetadataRegistry() *MetadataRegistry {
	return &MetadataRegistry{
		entities:          map[string]EntityDefinition{},
		relatedViews:      map[string][]RelatedView{},
		fieldDisplayHints: map[string][]FieldDisplayHint{},
	}
}

func (r *MetadataRegistry) Register(entity EntityDefinition) error {
	if _, ok := r.entities[entity.Name]; ok {
		return alreadyRegisteredErr(entity.Name)
	}
	if err := Validate(entity); err != nil {
		return validationErr(err.(*ValidationError))
	}
	r.entities[entity.Name] = entity
	return nil
}

// RegisterAllSubmitted registers every entity submitted via SubmitEntity anywhere in this
// binary's import graph, plus every SubmitRelatedViews/SubmitFieldDisplayHints submission.
func (r *MetadataRegistry) RegisterAllSubmitted() error {
	submissionsMu.Lock()
	entityFactories := append([]func() EntityDefinition{}, submittedEntityFactories...)
	relatedViews := append([]relatedViewsSubmission{}, submittedRelatedViews...)
	fieldDisplayHints := append([]fieldDisplayHintsSubmission{}, submittedFieldDisplayHints...)
	submissionsMu.Unlock()

	for _, factory := range entityFactories {
		if err := r.Register(factory()); err != nil {
			return err
		}
	}
	for _, sub := range relatedViews {
		r.relatedViews[sub.entity] = append(r.relatedViews[sub.entity], sub.factory()...)
	}
	for _, sub := range fieldDisplayHints {
		r.fieldDisplayHints[sub.entity] = append(r.fieldDisplayHints[sub.entity], sub.factory()...)
	}
	return nil
}

func (r *MetadataRegistry) GetEntity(name string) (EntityDefinition, bool) {
	e, ok := r.entities[name]
	return e, ok
}

func (r *MetadataRegistry) GetRelatedViews(name string) []RelatedView {
	return r.relatedViews[name]
}

func (r *MetadataRegistry) GetFieldDisplayHints(name string) []FieldDisplayHint {
	return r.fieldDisplayHints[name]
}

// MergeWith clones this registry and registers extra on top of it — used to merge a
// code-authored base registry with entities loaded from another source without either source
// knowing about the other.
func (r *MetadataRegistry) MergeWith(extra []EntityDefinition) (*MetadataRegistry, error) {
	merged := &MetadataRegistry{
		entities:          cloneEntityMap(r.entities),
		relatedViews:      cloneRelatedViewsMap(r.relatedViews),
		fieldDisplayHints: cloneFieldDisplayHintsMap(r.fieldDisplayHints),
	}
	for _, entity := range extra {
		if err := merged.Register(entity); err != nil {
			return nil, err
		}
	}
	return merged, nil
}

func cloneEntityMap(m map[string]EntityDefinition) map[string]EntityDefinition {
	out := make(map[string]EntityDefinition, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
func cloneRelatedViewsMap(m map[string][]RelatedView) map[string][]RelatedView {
	out := make(map[string][]RelatedView, len(m))
	for k, v := range m {
		out[k] = append([]RelatedView{}, v...)
	}
	return out
}
func cloneFieldDisplayHintsMap(m map[string][]FieldDisplayHint) map[string][]FieldDisplayHint {
	out := make(map[string][]FieldDisplayHint, len(m))
	for k, v := range m {
		out[k] = append([]FieldDisplayHint{}, v...)
	}
	return out
}

func (r *MetadataRegistry) ValidateReferences() error {
	for _, entity := range r.entities {
		var issues []string
		for _, field := range entity.Fields {
			if field.Kind != KindReference || field.RefEntity == nil {
				continue
			}
			refEntityName := *field.RefEntity
			target, ok := r.entities[refEntityName]
			if !ok {
				issues = append(issues, fmt.Sprintf("field %q references unknown entity %q", field.Name, refEntityName))
				continue
			}
			if field.RefDisplayField != nil {
				found := false
				for _, f := range target.Fields {
					if f.Name == *field.RefDisplayField {
						found = true
						break
					}
				}
				if !found {
					issues = append(issues, fmt.Sprintf(
						"field %q has refDisplayField %q which does not exist on %q", field.Name, *field.RefDisplayField, refEntityName))
				}
			}
		}
		if len(issues) > 0 {
			return &ValidationError{Entity: entity.Name, Issues: issues}
		}
	}
	return nil
}

func (r *MetadataRegistry) GetEntityMetadata(name string) (EntitySummary, bool) {
	entity, ok := r.entities[name]
	if !ok {
		return EntitySummary{}, false
	}
	return r.toSummary(entity), true
}

func (r *MetadataRegistry) ListEntities() []EntitySummary {
	out := make([]EntitySummary, 0, len(r.entities))
	for _, entity := range r.entities {
		out = append(out, r.toSummary(entity))
	}
	return out
}

func (r *MetadataRegistry) toSummary(entity EntityDefinition) EntitySummary {
	version, err := Hash(entity)
	if err != nil {
		// Hashing a plain struct of string/bool/slice fields cannot fail in practice — panic
		// rather than silently emit a wrong/empty version, matching the Rust .expect().
		panic(fmt.Sprintf("hashing entity metadata cannot fail: %v", err))
	}
	return EntitySummary{
		Name: entity.Name, Label: entity.Label, Fields: entity.Fields, ListViews: entity.ListViews,
		Workflow: entity.Workflow, RelatedViews: r.relatedViews[entity.Name], FieldDisplayHints: r.fieldDisplayHints[entity.Name],
		UniqueConstraints: entity.UniqueConstraints, Audit: entity.Audit, Version: version,
	}
}
