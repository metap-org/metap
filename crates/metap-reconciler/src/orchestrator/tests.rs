use super::*;
use metap_metadata::EntityField;

fn claimed(tenant: Uuid, entity_name: &str) -> ClaimedEntity {
    ClaimedEntity {
        tenant_id: tenant,
        entity_name: entity_name.to_string(),
        desired_version: 1,
    }
}

fn def(name: &str, fields: Vec<EntityField>) -> EntityDefinition {
    EntityDefinition {
        name: name.to_string(),
        label: name.to_string(),
        table_name: "records".to_string(),
        fields,
        list_views: vec![],
        workflow: None,
    }
}

fn plain_field(name: &str) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: name.to_string(),
        kind: FieldKind::String,
        required: None,
        indexed: None,
        unique: None,
        enum_values: None,
        ref_entity: None,
        ref_display_field: None,
        searchable: None,
        search_mode: None,
        sortable: None,
        storage: None,
        min: None,
        max: None,
        min_length: None,
        max_length: None,
    }
}

fn reference_field(name: &str, ref_entity: &str) -> EntityField {
    EntityField {
        kind: FieldKind::Reference,
        ref_entity: Some(ref_entity.to_string()),
        ..plain_field(name)
    }
}

fn wave_names(waves: &[Vec<ClaimedEntity>]) -> Vec<Vec<String>> {
    waves
        .iter()
        .map(|wave| {
            let mut names: Vec<String> = wave.iter().map(|e| e.entity_name.clone()).collect();
            names.sort();
            names
        })
        .collect()
}

#[test]
fn topo_sort_orders_referenced_entity_before_referencer() {
    let tenant = Uuid::new_v4();
    let sprints = def("jira.sprints", vec![plain_field("name")]);
    let issues = def("jira.issues", vec![reference_field("sprint", "jira.sprints")]);
    let waves = topo_sort_waves(vec![
        (claimed(tenant, "jira.issues"), issues),
        (claimed(tenant, "jira.sprints"), sprints),
    ]);
    assert_eq!(
        wave_names(&waves),
        vec![vec!["jira.sprints".to_string()], vec!["jira.issues".to_string()]]
    );
}

#[test]
fn topo_sort_puts_independent_entities_in_the_same_wave() {
    let tenant = Uuid::new_v4();
    let a = def("a", vec![plain_field("name")]);
    let b = def("b", vec![plain_field("name")]);
    let waves = topo_sort_waves(vec![(claimed(tenant, "a"), a), (claimed(tenant, "b"), b)]);
    assert_eq!(wave_names(&waves), vec![vec!["a".to_string(), "b".to_string()]]);
}

#[test]
fn topo_sort_ignores_reference_to_an_entity_outside_the_batch() {
    let tenant = Uuid::new_v4();
    // Depends on "jira.projects", which wasn't claimed this round — no ordering to enforce,
    // its table is assumed to already exist from an earlier reconcile.
    let issues = def("jira.issues", vec![reference_field("project", "jira.projects")]);
    let waves = topo_sort_waves(vec![(claimed(tenant, "jira.issues"), issues)]);
    assert_eq!(wave_names(&waves), vec![vec!["jira.issues".to_string()]]);
}

#[test]
fn topo_sort_does_not_order_across_different_tenants() {
    let tenant_a = Uuid::new_v4();
    let tenant_b = Uuid::new_v4();
    let sprints = def("jira.sprints", vec![plain_field("name")]);
    let issues = def("jira.issues", vec![reference_field("sprint", "jira.sprints")]);
    // Tenant B only has the referencing entity claimed (its "jira.sprints" table already
    // exists from before) — it must land in wave 0, not wait on tenant A's wave 1.
    let waves = topo_sort_waves(vec![
        (claimed(tenant_a, "jira.issues"), issues.clone()),
        (claimed(tenant_a, "jira.sprints"), sprints),
        (claimed(tenant_b, "jira.issues"), issues),
    ]);
    assert_eq!(waves.len(), 2);
    assert_eq!(waves[0].len(), 2); // tenant A's sprints + tenant B's issues
    assert_eq!(waves[1].len(), 1); // tenant A's issues
}

#[test]
fn topo_sort_falls_back_to_one_wave_on_a_dependency_cycle() {
    let tenant = Uuid::new_v4();
    let a = def("a", vec![reference_field("b_ref", "b")]);
    let b = def("b", vec![reference_field("a_ref", "a")]);
    let waves = topo_sort_waves(vec![(claimed(tenant, "a"), a), (claimed(tenant, "b"), b)]);
    assert_eq!(wave_names(&waves), vec![vec!["a".to_string(), "b".to_string()]]);
}

#[test]
fn wave_size_is_canary_then_percentages_capped_at_total() {
    assert_eq!(wave_size(0, 0), 0);
    assert_eq!(wave_size(1, 0), 1);
    assert_eq!(wave_size(1, 3), 1);
    assert_eq!(wave_size(100, 0), 2);
    assert_eq!(wave_size(100, 1), 5);
    assert_eq!(wave_size(100, 2), 25);
    assert_eq!(wave_size(100, 3), 100);
    // Canary floor still applies even when 5%/25% of a small fleet would be smaller than it.
    assert_eq!(wave_size(10, 1), 2);
    assert_eq!(wave_size(10, 2), 3);
}

#[test]
fn wave_size_never_exceeds_total() {
    for total in [0, 1, 2, 3, 7, 40] {
        for wave in 0..5 {
            assert!(wave_size(total, wave) <= total);
        }
    }
}
