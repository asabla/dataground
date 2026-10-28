# Workbench delivery

This plan starts with complete user journeys through the existing agent-service
contracts. It follows ADR-003 and ADR-012. It does not change the initial-release
scope or establish Databricks compatibility.

## Service discovery

The service directory reads the authenticated isolation-scoped service list. Users
can search loaded names, descriptions and IDs, sort by name or metadata update,
refresh the list, and load additional pages. Search and counts explicitly describe
loaded resources, not a server-wide total. Service metadata is not used to infer
publication, readiness or active execution. A failed refresh keeps the last list
visible but disables its open actions until a successful read.

Each service offers setup and interaction-history entry points. Setup re-reads
revision and route state. History reads the selected service's invocation list
without requiring a current route or publication, so completed work remains
reachable after route withdrawal. Page changes move keyboard focus to the new
heading. Search and sort remain in memory across those changes; disconnect clears
the connection and its local state.

## Revision input authoring

Revision authoring offers an explicit prompt template with an optional label and
guidance. It produces a closed object with one required string property named
`prompt`, bounded to 1–262,144 characters, which the existing interaction form can
render. The template is opt-in and does not change runtime, capability or output
configuration. Guidance describes the form; it is not an agent instruction.

The JSON editor retains custom schemas. Guided mode recognizes only the exact
template and cannot discard custom validation rules, annotations or additional
properties. Switching between recognized guided and JSON views does not submit a
command. Existing revision validation, authority and exact-request retry rules
continue to apply. Publication and routing remain separate actions.

## Next product journeys

Interaction inspection must then provide a coherent path through progress,
questions, approvals, results and retained artifacts. Native governed execution
still needs its separate runtime acceptance and workspace-retention integration;
reference execution cannot prove those boundaries.

After the governed agent-service journey meets its acceptance gates, implement
the notebook, data and job journeys with their real APIs, persistence and runtime
support. Add navigation when a usable feature exists. Production identity,
deployment, accessibility and recovery acceptance remain release requirements.

## Verification boundaries

Directory unit and rendering tests cover loaded-page search, stable sorting,
untrusted names, empty results and stale-action disabling. Manual journey checks
use the built-in Browser against the local reference API with disposable data.
The repository CI remains the full automated gate. Neither these checks nor a
local preview establishes full-journey accessibility or production readiness.
