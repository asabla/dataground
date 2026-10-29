# Approved workbench redesign delivery

The user approved the [connected prototype](../design/workbench-redesign/README.md)
on 29 October 2026 and authorized autonomous implementation, self-review, and
rebase-and-merge of focused PRs. The prototype review gate is complete. The
prototype is a design reference; production features require their actual
contracts, authorization, persistence and runtime evidence.

## Delivery boundaries

Follow the decision register, system specification and ADR-003 governed-agent
prerequisite. Retain existing command implementations and their exact-request
recovery. Do not add a workflow engine for a graph, substitute an isolation domain
for workspace membership, or expose native runtime or credential endpoints.
Unimplemented or unauthorized areas do not appear as production destinations.
Local credentials stay local. Release certification remains separate from code
completion and from reference-runtime demonstrations.

## Implementation sequence

| Slice | Outcome | Contract and acceptance boundary | State |
| --- | --- | --- | --- |
| Resource table | Shared accessible grid with a compact service directory | Existing validated service pages and stale-action guards; Storybook keyboard, sorting and accessibility contracts | Merged in PR #372; shared tests, Browser keyboard/theme review and repository CI passed |
| Exact service read | Direct lookup without scanning list pages | Path-derived Cedar action, scoped PostgreSQL read and retained authorization evidence | Merged in PR #373; contract, client and repository CI checks passed |
| Navigation foundation | Scoped service URLs and browser history; connection, shell and workflow session separated | Exact authorized service reads; independent service contexts retained across navigation; identity changes clear state | Merged in PR #374, #375, #381, #384, #385, #387 and #389; service, revision, run and artifact URLs and retained drafts/commands are implemented |
| Shared workbench patterns | Tabs, menus, tree, dialogs, detail panels and resizable regions | Each component has a real consumer and Storybook contract; keyboard, focus and both themes | Tables, tabs, copy controls and bounded split views merged in PR #372, #376, #383 and #390 with consumers and Storybook contracts; publication dialog merged in PR #402; action menu merged in PR #404; trees remain |
| Agent authoring | Service directory and Overview, Build, Versions, Deployments, Runs views | Reuse creation, publication, routing, retirement and withdrawal commands; preserve drafts and uncertain requests | Five service views, compact version/route/run tables, authoring layout and revision retention merged through PR #389; focused withdrawal and retirement presentation merged in PR #394; command receipt focus and route-list reconciliation merged in PR #407. Route dialogs, run-list reconciliation and additional named routes merged in PR #408–#410. Repeat runs, retained recent locations, request reconciliation, cancellation dialogs and lifecycle focus return merged in PR #415 after native Browser review and repository CI passed |
| Run and artifact inspection | Outcome/blocker first, activity, decisions, results, files and audit in separate views | Preserve bounded authorized reads, explicit content disclosure, expiry, cancellation and late-response rejection | Run outcome, retained views, compact activity/request browsers, contextual inspection and artifact deep links merged through PR #390; paginated artifact metadata API and Files browser merged in PR #391 and #392; flat request inspection merged in PR #393; completed request details and cancellation dialogs merged in PR #415 |
| Agent migration acceptance | Complete replacement journey and temporary presentation comparison | Same command implementations; remove old presentation only after full journey acceptance | Classic presentation removed in PR #417 after controlled journey review and all repository CI checks passed |
| Governed execution prerequisite | Complete accepted runtime and workspace-retention integration | Exact-image acceptance and real local execution evidence; internal helper tests alone are insufficient | Pending |
| Scope run discovery | One authorized paginated list across agent services, with server-side state filters and exact detail links | Separate scope-level metadata permission; service, run and content reads remain independently authorized; no browser fan-out | API and retained Runs browser merged in PR #395 and #396 after repository CI passed |
| Scope artifact discovery | One authorized metadata browser across runs, with source links | Separate scope metadata permission; exact service, run, artifact and content reads remain independently authorized | API, browser and shared discovery controller merged in PR #399, #400 and #401 after repository CI passed |
| Home | Recent session work and bounded attention preview | Connection-local recents and authorized scope queries; saved-resource contracts remain separate | Merged in PR #403 after repository CI passed; persistent creation menu merged in PR #419 |
| Workspace/search summaries | Explicit membership, paginated search and cross-resource attention | New authorized public read models; no per-service browser fan-out | Workspace service relationship API and real Workspace creation, discovery and service navigation merged in PR #405 and #406. Server-side service search in Agents and Workspace merged in PR #416 after PostgreSQL, Browser and repository CI checks passed; workspace-list server search merged in PR #418 after PostgreSQL, Browser and repository CI checks passed. Cross-resource metadata search API and retained workbench consumer merged in PR #420 and #421. Files, notebooks and saved queries remain |
| Catalog and SQL | Discover, inspect, execute, cancel, view results/history and save | Real metadata/query services, bounded results, durable state and isolation tests | Pending |
| Notebooks | Versioned document editing, kernel interaction and retained output | Optimistic concurrency and the specified platform-mediated Jupyter contract | Pending |
| Jobs and operations | Definitions, runs, task inspection, supported recovery and administration | Durable resource-specific state machines; observed evidence and public administration contracts | Pending |

## Runtime and migration status

The source-worker conformance record documents development model-backed source
execution and the authenticated public publication/invocation journey under
short-lived local trust. It does not establish operational or production
acceptance. The workspace change exporter remains an internal controller:
accepted helper images, a complete writer fence, worker teardown integration,
retention resolution, ordered journals and checkpoints remain. See
[governed source conformance](../development/governed-source-conformance.md) and
[workspace change export](../development/workspace-change-export.md). Broad data,
notebook and job integration still follows that governed-agent prerequisite.

Classic presentation was removed in PR #417 after repository CI and the controlled replacement
journey checks recorded in [migration acceptance](../development/workbench-migration-acceptance.md).
Request, lifecycle, run and artifact views use the existing command and recovery
implementations. Fixture Browser checks prove navigation
and presentation; reference-API checks prove only that development profile.
Neither replaces representative-user testing or release certification.

## Verification and completion

Each PR must pass the relevant local checks and repository CI before merge. Use
the built-in Browser for manual visual and journey checks. Preserve the existing
isolation, authorization and recovery tests. Test pending and unknown commands,
failed refresh, stale responses, conflicting edits, scope/identity change,
keyboard navigation, focus return, zoom/reflow, both themes and long content.

Representative-user acceptance, complete accessibility conformance, production
identity/deployment and runtime certification remain explicit release gates.
This ledger records concrete delivery; it does not treat design approval as
approval of unimplemented production behavior.
