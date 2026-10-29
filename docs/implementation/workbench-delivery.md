# Workbench delivery

This document records the agent-workbench journeys. The
[approved redesign delivery](workbench-redesign-delivery.md) tracks the current
migration and remaining product areas. Both follow ADR-003 and ADR-012. Neither
changes the initial-release scope or establishes Databricks compatibility.

## Service discovery

The service directory reads the authenticated isolation-scoped service list.
Search matches names, descriptions and IDs before pagination. Sorting applies to
loaded results, and counts do not imply a server-wide total. Service metadata is
not used to infer publication, readiness or active execution. Refresh clears old
rows and invalidates their actions. A failed read does not restore those actions.

Each service has Overview, Build, Versions, Deployments and Runs views with stable
URLs. Revision and route reads remain separate from service metadata. Runs reads the selected service's invocation list
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

Before confirmation, publication review can reveal the exact loaded input and
output contracts as bounded plain-text JSON. It also reports whether the
workbench can render the input contract. This is separate from publication
validation. Review content is hidden by default, removed on hide, and bound to
the exact loaded revision object and connection. A changed scope, version,
connection or loaded object closes it. Pending-request recovery uses the retained
definition, and accepted or completed publication hides the review.

## Interaction inspection

Run inspection presents the outcome or blocker before Activity, Requests, Output,
Files and Audit tabs. Selecting a question, approval or artifact opens contextual
inspection and moves focus to it. Closing inspection returns focus to its source
when that source remains available in the same connection and run; otherwise it
returns to the current view. Retained views preserve unfinished answers and exact
request recovery. Content requires an explicit read and is cleared when hidden or
when its identity changes. Resource reads and commands retain their existing scope
and authorization checks.

## Next product journeys

Native governed execution still needs its separate runtime acceptance and
workspace-retention integration; reference execution cannot prove those boundaries.

After the governed agent-service journey meets its acceptance gates, implement
the notebook, data and job journeys with their real APIs, persistence and runtime
support. Add navigation when a usable feature exists. Production identity,
deployment, accessibility and recovery acceptance remain release requirements.

## Verification boundaries

Contract, database, client and journey tests cover search before pagination,
scope isolation, stable sorting, empty results and stale-action rejection. Manual
journey checks use the built-in Browser against isolated reference and durable
development APIs with fictional data.
The repository CI remains the full automated gate. Neither these checks nor a
local preview establishes full-journey accessibility or production readiness.
