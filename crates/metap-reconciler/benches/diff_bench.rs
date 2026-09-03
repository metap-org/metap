//! Micro-benchmark for `compile()`+`diff()` — the pure schema-comparison path `reconcile()` runs
//! every tick (no DB I/O; `execute()` against a real Postgres is what `reconcile_postgres.rs`'s
//! e2e tests already cover). Two shapes matter in practice: **cold** (`actual: None`, every op
//! gets created — a brand-new entity's first reconcile) and **converged** (`actual` equals
//! `desired` — the steady-state case every subsequent tick hits, must stay cheap since
//! `metap-reconciler`'s whole level-triggered design assumes converged reconciles are near-free).

use std::hint::black_box;

use criterion::{criterion_group, criterion_main, Criterion};
use metap_metadata::{EntityDefinition, EntityField, EntityListView, FieldKind, FieldStorage};
use metap_reconciler::{compile, diff, qualified_table_name_for};

fn field(name: &str, kind: FieldKind, indexed: bool, sortable: bool, searchable: bool) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: name.to_string(),
        kind,
        required: None,
        indexed: indexed.then_some(true),
        unique: None,
        enum_values: matches!(kind, FieldKind::Enum).then(|| vec!["a".to_string(), "b".to_string(), "c".to_string()]),
        ref_entity: matches!(kind, FieldKind::Reference).then(|| "bench.other".to_string()),
        ref_display_field: None,
        searchable: searchable.then_some(true),
        search_mode: None,
        sortable: sortable.then_some(true),
        storage: None,
        min: None,
        max: None,
        min_length: None,
        max_length: None,
        computed: None,
    }
}

fn bench_entity() -> EntityDefinition {
    let mut money_field = field("budget", FieldKind::Money, false, false, false);
    money_field.storage = Some(FieldStorage::Column);

    EntityDefinition {
        name: "bench.issues".to_string(),
        label: "Bench Issue".to_string(),
        table_name: qualified_table_name_for("bench.issues"),
        fields: vec![
            field("title", FieldKind::String, false, false, true),
            field("status", FieldKind::Enum, true, true, false),
            field("priority", FieldKind::Enum, true, true, false),
            field("project", FieldKind::Reference, true, false, false),
            field("storyPoints", FieldKind::Number, false, true, false),
            field("dueDate", FieldKind::Date, false, true, false),
            money_field,
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["title".to_string(), "status".to_string()],
            filters: vec!["status".to_string(), "priority".to_string()],
            default_sort: None,
            max_limit: 100,
        }],
        workflow: None,
    }
}

fn bench(c: &mut Criterion) {
    let entity = bench_entity();
    let desired = compile(&entity).expect("valid entity compiles");
    let converged_actual = desired.clone();

    c.bench_function("reconciler/compile", |b| {
        b.iter(|| {
            let schema = compile(black_box(&entity)).unwrap();
            black_box(schema);
        });
    });

    c.bench_function("reconciler/diff_cold_create_everything", |b| {
        b.iter(|| {
            let ops = diff(black_box(&desired), black_box(None), black_box(&[]));
            black_box(ops);
        });
    });

    c.bench_function("reconciler/diff_converged_zero_ops", |b| {
        b.iter(|| {
            let ops = diff(black_box(&desired), black_box(Some(&converged_actual)), black_box(&[]));
            black_box(ops);
        });
    });
}

criterion_group!(benches, bench);
criterion_main!(benches);
