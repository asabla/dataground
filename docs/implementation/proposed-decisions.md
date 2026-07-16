# Proposed implementation decisions

These are not confirmed ADRs. They are the smallest remaining decisions needed to make implementation predictable.

## P-001 — Core implementation stack

**Recommendation:** Go control plane and workers; TypeScript/React workbench; thin TypeScript/Python native runtime sidecars only where upstream SDKs justify them.

**Why:** strong controller/concurrency ecosystem for lifecycle services, a standard frontend stack, and minimal translation around upstream SDKs. It accepts the unavoidable runtime-language edges without spreading them into the platform domain model.

**Evidence spike:** implement resource CRUD, one transactional outbox worker, SSE replay, a fake runtime adapter and one UI event timeline. Score cognitive load, generated contracts, debugging, test speed, deployment footprint and cross-language schema drift.

## P-002 — API/schema toolchain

**Recommendation:** OpenAPI 3.1 and JSON Schema 2020-12 as canonical public schemas; generated clients are reproducible outputs, not hand-edited sources.

**Why:** aligns REST/resource contracts, schema-driven forms and event validation without adopting an internal RPC technology before it is needed.

**Evidence spike:** generate Go and TypeScript types/clients, validate examples, detect breaking changes, and render the invocation form from the same schema.

## P-003 — Design-system architecture

**Recommendation:** owned DTCG-format semantic tokens, React Aria Components if React is selected for the workbench, CSS custom-property output, and Storybook contract/testing.

**Why:** separates DataGround semantics and visual identity from the accessibility primitive implementation, supports themes/density, and limits dependence on a copied component catalog.

**Evidence spike:** build the workbench shell, service header, event timeline, question/approval panel and invocation form; run keyboard, screen-reader, RTL, zoom and automated accessibility checks.

## P-004 — Initial deployable shape

**Recommendation:** modular monolith API plus replaceable worker processes, callback dispatcher and runtime adapter sidecars. Do not start with many microservices.

**Why:** ADR-011 requires independently deployable units but does not require premature distribution. Trust boundaries and scaling evidence should drive splits.

**Exit trigger for a split:** distinct trust boundary, scaling pattern, failure domain, runtime dependency or release cadence demonstrated by evidence.

## P-005 — First real coding runtime

**Recommendation:** select through a bounded conformance spike with explicit exit criteria, not by preference. Codex app-server is a strong candidate because the confirmed architecture already selects its rich native event interface, but the choice must account for available authentication, target use case and pinned-version stability.

The first adapter is an implementation sequence choice only. It does not demote the other three required runtime families.

## P-006 — Reference object store, catalog and identity provider

**Recommendation:** make three separate, scored selection decisions after conformance spikes. Do not embed vendor-specific APIs into the core.

**Required outputs:** decision record, pinned version/digest, license analysis, conformance results, backup/restore evidence, upgrade evidence and replacement plan.

## P-007 — UI content and terminology governance

**Recommendation:** maintain a versioned product glossary and status/event vocabulary next to the public schemas. UI copy uses DataGround resource names and never exposes provider implementation nouns except in an explicitly technical diagnostic view.

**Why:** the platform normalizes multiple runtimes, gateways and state machines; inconsistent terminology becomes a correctness and safety defect, especially around approvals and observed state.
