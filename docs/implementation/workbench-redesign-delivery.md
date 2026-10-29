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
| Navigation foundation | Scoped service URLs and browser history; connection, shell and workflow session separated | Exact authorized service reads; independent service contexts retained across navigation; identity changes clear state | Initial service navigation and per-service command sessions implemented; revision/run/artifact URLs and revision-specific retention remain |
| Shared workbench patterns | Tabs, menus, tree, dialogs, detail panels and resizable regions | Each component has a real consumer and Storybook contract; keyboard, focus and both themes | Pending |
| Agent authoring | Service directory and Overview, Build, Versions, Deployments, Runs views | Reuse creation, publication, routing, retirement and withdrawal commands; preserve drafts and uncertain requests | Pending |
| Run and artifact inspection | Outcome/blocker first, activity, decisions, results, files and audit in separate views | Preserve bounded authorized reads, explicit content disclosure, expiry, cancellation and late-response rejection | Pending |
| Agent migration acceptance | Complete replacement journey and temporary presentation comparison | Same command implementations; remove old presentation only after full journey acceptance | Pending |
| Governed execution prerequisite | Complete accepted runtime and workspace-retention integration | Exact-image acceptance and real local execution evidence; internal helper tests alone are insufficient | Pending |
| Workspace/search summaries | Explicit membership, paginated search and cross-resource attention | New authorized public read models; no per-service browser fan-out | Pending |
| Catalog and SQL | Discover, inspect, execute, cancel, view results/history and save | Real metadata/query services, bounded results, durable state and isolation tests | Pending |
| Notebooks | Versioned document editing, kernel interaction and retained output | Optimistic concurrency and the specified platform-mediated Jupyter contract | Pending |
| Jobs and operations | Definitions, runs, task inspection, supported recovery and administration | Durable resource-specific state machines; observed evidence and public administration contracts | Pending |

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
