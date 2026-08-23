//! Reusable sustained-load harness — the worker-spawn/percentile/reporting machinery that
//! `sustained_concurrent_list_against_a_real_multi_entity_abac_workflow` and
//! `sustained_concurrent_list_across_many_tenants_at_ten_million_rows` (`crud_service_postgres.rs`)
//! used to each duplicate byte-for-byte (found while building `testing/`'s performance pillar,
//! `testing/README.md`). Only what actually varied between them — entity/tenant setup and what
//! one iteration does — stays at the call site; everything else (spawn, deadline loop, latency
//! collection, percentiles, the results printout) lives here once.
//!
//! Not a separate test binary — `tests/support/` is a plain module, only compiled when a
//! `tests/*.rs` file declares `mod support;` (standard Rust integration-test convention), so
//! this never runs as its own `#[test]`.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

pub struct LoadTestConfig {
    pub duration: Duration,
    pub concurrency: usize,
}

pub struct LoadTestReport {
    pub label: String,
    pub ok: u64,
    pub err: u64,
    pub elapsed: Duration,
    total_latency_us: u64,
    latencies_ms: Vec<u64>,
}

impl LoadTestReport {
    pub fn throughput_per_sec(&self) -> f64 {
        self.ok as f64 / self.elapsed.as_secs_f64()
    }

    fn percentile(&self, p: f64) -> u64 {
        if self.latencies_ms.is_empty() {
            return 0;
        }
        let idx = ((self.latencies_ms.len() as f64 - 1.0) * p) as usize;
        self.latencies_ms[idx]
    }

    pub fn p50_ms(&self) -> u64 {
        self.percentile(0.50)
    }
    pub fn p95_ms(&self) -> u64 {
        self.percentile(0.95)
    }
    pub fn p99_ms(&self) -> u64 {
        self.percentile(0.99)
    }
    pub fn max_ms(&self) -> u64 {
        self.latencies_ms.last().copied().unwrap_or(0)
    }
    pub fn avg_latency_ms(&self) -> f64 {
        self.total_latency_us as f64 / self.ok.max(1) as f64 / 1000.0
    }

    /// Same shape/labels the two original sustained tests each printed by hand.
    pub fn print_summary(&self) {
        eprintln!("=== {} results ===", self.label);
        eprintln!("duration: {:.1}s", self.elapsed.as_secs_f64());
        eprintln!("total calls: {} ok, {} err", self.ok, self.err);
        eprintln!("throughput: {:.2} calls/s", self.throughput_per_sec());
        eprintln!(
            "latency ms: avg={:.1} p50={} p95={} p99={} max={}",
            self.avg_latency_ms(),
            self.p50_ms(),
            self.p95_ms(),
            self.p99_ms(),
            self.max_ms()
        );
    }

    /// Convenience for the common case (every call site so far wants exactly this assertion).
    pub fn assert_no_errors(&self) {
        assert_eq!(self.err, 0, "no call should have failed during {}", self.label);
    }
}

/// Spawns `config.concurrency` tokio tasks, each repeatedly calling
/// `iteration(worker_index, iteration_index)` until `config.duration` has elapsed since the
/// first call started (a shared deadline, not a per-worker one — matches the original tests'
/// `while Instant::now() < deadline` loop). `iteration` returns the latency to record on
/// success; an `Err` is counted and logged but never stops the run (one bad call must not end
/// the whole sustained-load measurement).
pub async fn run_sustained_load<F, Fut>(
    label: impl Into<String>,
    config: LoadTestConfig,
    iteration: F,
) -> LoadTestReport
where
    F: Fn(usize, usize) -> Fut + Send + Sync + 'static,
    Fut: std::future::Future<Output = anyhow::Result<()>> + Send,
{
    let iteration = Arc::new(iteration);
    let total_ok = Arc::new(AtomicU64::new(0));
    let total_err = Arc::new(AtomicU64::new(0));
    let total_latency_us = Arc::new(AtomicU64::new(0));
    let mut latencies_ms: Vec<Vec<u64>> = (0..config.concurrency).map(|_| Vec::new()).collect();

    let start = Instant::now();
    let deadline = start + config.duration;

    let mut handles = Vec::with_capacity(config.concurrency);
    for worker in 0..config.concurrency {
        let iteration = iteration.clone();
        let total_ok = total_ok.clone();
        let total_err = total_err.clone();
        let total_latency_us = total_latency_us.clone();
        handles.push(tokio::spawn(async move {
            let mut my_latencies = Vec::new();
            let mut i: usize = 0;
            while Instant::now() < deadline {
                let call_start = Instant::now();
                let result = iteration(worker, i).await;
                let elapsed = call_start.elapsed();
                match result {
                    Ok(()) => {
                        total_ok.fetch_add(1, Ordering::Relaxed);
                        total_latency_us.fetch_add(elapsed.as_micros() as u64, Ordering::Relaxed);
                        my_latencies.push(elapsed.as_millis() as u64);
                    }
                    Err(err) => {
                        total_err.fetch_add(1, Ordering::Relaxed);
                        eprintln!("worker {worker} iteration {i} error: {err:#}");
                    }
                }
                i += 1;
            }
            my_latencies
        }));
    }

    for (worker, handle) in handles.into_iter().enumerate() {
        latencies_ms[worker] = handle.await.unwrap();
    }

    let elapsed = start.elapsed();
    let mut all_latencies: Vec<u64> = latencies_ms.into_iter().flatten().collect();
    all_latencies.sort_unstable();

    LoadTestReport {
        label: label.into(),
        ok: total_ok.load(Ordering::Relaxed),
        err: total_err.load(Ordering::Relaxed),
        elapsed,
        total_latency_us: total_latency_us.load(Ordering::Relaxed),
        latencies_ms: all_latencies,
    }
}
