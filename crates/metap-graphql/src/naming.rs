//! Entity name (`"crm.customers"`) -> GraphQL identifier conversions. GraphQL type/field names
//! can't contain `.`, so every name derived from an `EntityDefinition.name` goes through these —
//! used consistently by `schema.rs` so a type name and its corresponding query/mutation field
//! names can never drift apart from each other.

fn pascal_case(entity_name: &str) -> String {
    entity_name
        .split(['.', '_', '-'])
        .map(|part| {
            let mut chars = part.chars();
            match chars.next() {
                Some(first) => first.to_uppercase().collect::<String>() + chars.as_str(),
                None => String::new(),
            }
        })
        .collect()
}

fn camel_case(entity_name: &str) -> String {
    let pascal = pascal_case(entity_name);
    let mut chars = pascal.chars();
    match chars.next() {
        Some(first) => first.to_lowercase().collect::<String>() + chars.as_str(),
        None => pascal,
    }
}

/// The entity's GraphQL `Object` type name — e.g. `"crm.customers"` -> `"CrmCustomers"`.
pub fn type_name(entity_name: &str) -> String {
    pascal_case(entity_name)
}

/// The `{Type}Connection` wrapper type `list_field` returns.
pub fn connection_type_name(entity_name: &str) -> String {
    format!("{}Connection", type_name(entity_name))
}

/// `Query.{camel}(id: ID!)` — single-record fetch.
pub fn get_field_name(entity_name: &str) -> String {
    camel_case(entity_name)
}

/// `Query.{camel}List(...)` — deliberately `{camel}List` rather than a pluralized form
/// (`{camel}s`): English pluralization is ambiguous/wrong often enough (an entity name is
/// whatever a low-code author typed, not guaranteed to be a regular plural-able noun) that a
/// fixed suffix is more predictable than guessing, at the cost of being slightly less
/// idiomatic-looking GraphQL.
pub fn list_field_name(entity_name: &str) -> String {
    format!("{}List", camel_case(entity_name))
}

pub fn create_field_name(entity_name: &str) -> String {
    format!("create{}", type_name(entity_name))
}

pub fn update_field_name(entity_name: &str) -> String {
    format!("update{}", type_name(entity_name))
}

pub fn delete_field_name(entity_name: &str) -> String {
    format!("delete{}", type_name(entity_name))
}

pub fn transition_field_name(entity_name: &str) -> String {
    format!("transition{}", type_name(entity_name))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dotted_entity_names_convert_to_pascal_and_camel_case() {
        assert_eq!(type_name("crm.customers"), "CrmCustomers");
        assert_eq!(get_field_name("crm.customers"), "crmCustomers");
        assert_eq!(list_field_name("crm.customers"), "crmCustomersList");
        assert_eq!(create_field_name("crm.customers"), "createCrmCustomers");
        assert_eq!(connection_type_name("crm.customers"), "CrmCustomersConnection");
    }

    #[test]
    fn underscored_field_style_entity_names_also_convert() {
        assert_eq!(type_name("jira.issue_links"), "JiraIssueLinks");
    }
}
