package metadata

// Entity-declaration DSL. Every service used to carry its own copy of field/enumField/refField/
// boolPtr/strPtr with a different argument order; declare fields with these instead:
//
//	metadata.NewField("title", "Title", metadata.KindString).Require().Index()
//	metadata.EnumField("status", "Status", "open", "closed").Require()
//	metadata.RefField("zoneId", "Zone", "waf.zones", "hostname").Require().Index()
//
// Each modifier returns a copy, so a base field can be reused and tweaked safely.

// Ptr returns a pointer to v, for the optional (*T) members of the metadata structs.
func Ptr[T any](v T) *T { return &v }

// NewField declares a plain field of the given kind.
func NewField(name, label string, kind FieldKind) EntityField {
	return EntityField{Name: name, Label: label, Kind: kind}
}

// EnumField declares an enum field with its allowed values.
func EnumField(name, label string, values ...string) EntityField {
	f := NewField(name, label, KindEnum)
	f.EnumValues = values
	return f
}

// RefField declares a reference to another entity; displayField is the referenced entity's field
// shown in lists and pickers.
func RefField(name, label, refEntity, displayField string) EntityField {
	f := NewField(name, label, KindReference)
	f.RefEntity = Ptr(refEntity)
	f.RefDisplayField = Ptr(displayField)
	return f
}

// Require marks the field required.
func (f EntityField) Require() EntityField { f.Required = Ptr(true); return f }

// Optional clears a required mark (for a field derived from a required base).
func (f EntityField) Optional() EntityField { f.Required = nil; return f }

// Index marks the field indexed.
func (f EntityField) Index() EntityField { f.Indexed = Ptr(true); return f }

// Sort marks the field sortable.
func (f EntityField) Sort() EntityField { f.Sortable = Ptr(true); return f }

// Search marks the field searchable.
func (f EntityField) Search() EntityField { f.Searchable = Ptr(true); return f }

// Uniq marks the field unique within a tenant.
func (f EntityField) Uniq() EntityField { f.Unique = Ptr(true); return f }

// UniqGlobal marks the field unique across every tenant sharing the table.
func (f EntityField) UniqGlobal() EntityField {
	f.Unique = Ptr(true)
	f.UniqueScope = Ptr(UniqueScopeGlobal)
	return f
}

// SystemOnly marks the field as written by the platform, never by a client payload.
func (f EntityField) SystemOnly() EntityField { f.System = Ptr(true); return f }

// Transition declares an unguarded workflow transition.
func Transition(action, from, to, label string) WorkflowTransition {
	return WorkflowTransition{Action: action, From: from, To: to, Label: label}
}
