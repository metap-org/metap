# Architecture

This folder documents Metap's architecture using three complementary frameworks, not one:

- **[arc42](https://arc42.org)** is the *documentation skeleton* — the 12 numbered files in this folder, one per arc42 section, are this project's answer to "why is it built this way, and where do I read about X."
- **[C4](https://c4model.com)** is the *diagram notation* — used inside the arc42 sections it fits naturally: System Context ([03](03-context.md)), Container + Component ([05](05-building-blocks.md)).
- **[Kruchten's 4+1 View Model](https://en.wikipedia.org/wiki/4%2B1_architectural_view_model)** is the *thinking model* — its five viewpoints aren't a separate section; they're folded into whichever arc42 section covers the same ground: Logical + Development → [05](05-building-blocks.md), Process + Scenarios ("+1") → [06](06-runtime.md), Physical → [07](07-deployment.md).

None of these compete — arc42 organizes the document, C4 draws the pictures, 4+1 makes sure no viewpoint (static structure, runtime behavior, source organization, deployment) got skipped.

## Sections

1. [Introduction and Goals](01-introduction.md) — vision, requirements overview, stakeholders, top quality goals
2. [Architecture Constraints](02-constraints.md) — technical, organizational, and convention constraints
3. [System Scope and Context](03-context.md) — business/technical context, C4 System Context
4. [Solution Strategy](04-strategy.md) — the fundamental decisions and why
5. [Building Block View](05-building-blocks.md) — C4 Container + Component, Logical View, core services, data model, DB design, service boundaries, Development View
6. [Runtime View](06-runtime.md) — Process View sequence diagram, key scenarios
7. [Deployment View](07-deployment.md) — Physical View, local dev topology
8. [Cross-cutting Concepts](08-cross-cutting.md) — patterns spanning multiple building blocks; security and performance principles
9. [Architecture Decisions](09-adr.md) — decision log (formerly indexed `docs/superpowers/specs/`, removed 2026-08-07; now records decisions directly)
10. [Quality Requirements](10-quality.md) — quality tree and concrete, testable scenarios
11. [Risks and Technical Debt](11-risks.md) — honest, trigger-based
12. [Glossary](12-glossary.md)

For the phased build-out (what's done, what's next), see `docs/roadmap.md`. For stack/technology reasoning, see `docs/why.md`. For where this is all headed — the low-code direction, and a concrete path toward a first version of it — see `docs/vision.md` and `docs/low-code-platform-v1.md`; both are deliberately kept outside this arc42 set since they describe a target, not what has shipped. `docs/modular-spi-architecture.md` describes a related, still-undecided target: a Capability SPI boundary (Storage/EventBus/Scheduler/...) that would let the same source run as a single-binary/SQLite "Tiny" deployment or a distributed enterprise one — also directional, not built.
