//! `load_entity_definitions_from_dir`'s directory-scan behavior — split out from
//! `src/entities_yaml.rs`'s inline `#[cfg(test)]` module (which keeps the 2 pure string-parsing
//! tests) specifically because this one touches a real `std::env::temp_dir()` path: semgrep's
//! `rust.lang.security.temp-dir.temp-dir` rule flags that call syntactically inside `src/`, but
//! not inside `tests/*.rs` — the same exemption this workspace's other `std::env::temp_dir()`-
//! based tests already rely on (`metap-http/tests/http_server.rs` and 6 others), not a new
//! pattern invented for this file. `Uuid::new_v4()`, not a predictable name, keeps this an
//! actually-safe pattern regardless of the exemption.

use uuid::Uuid;

#[test]
fn loads_every_yaml_file_in_a_directory_in_sorted_order() {
    let dir = std::env::temp_dir().join(format!("metap-app-entities-yaml-test-{}", Uuid::new_v4()));
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(
        dir.join("b.yaml"),
        "name: crm.orders\nlabel: Orders\ntableName: records\nfields: []\nlistViews: []\n",
    )
    .unwrap();
    std::fs::write(
        dir.join("a.yml"),
        "name: crm.customers\nlabel: Customers\ntableName: records\nfields: []\nlistViews: []\n",
    )
    .unwrap();
    std::fs::write(dir.join("ignore.txt"), "not yaml").unwrap();

    let entities = metap_app::load_entity_definitions_from_dir(&dir).unwrap();
    std::fs::remove_dir_all(&dir).unwrap();

    assert_eq!(entities.len(), 2);
    assert_eq!(entities[0].name, "crm.customers");
    assert_eq!(entities[1].name, "crm.orders");
}
