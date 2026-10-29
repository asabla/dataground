# Agent workbench migration acceptance

The prototype was approved on 29 October 2026. The production replacement was
reviewed through the built-in Browser using fictional data, a loopback reference
API, and a separate PostgreSQL-backed review API. These checks support removal
of the temporary classic presentation. They establish the agent workbench
interaction boundary, not complete product or runtime release acceptance.

| Journey | Observed evidence | Limit |
| --- | --- | --- |
| Create and configure a service | Reference API creation, retained revision draft, publication review, explicit route assignment, named routes and repeat invocations | Reference execution is deterministic; it is not native agent execution |
| Inspect runs and recover | Outcome and request views, exact run navigation, cancellation confirmation and completion, lifecycle return focus, retained recent location | Ambiguous responses and connection replacement also require the automated recovery tests |
| Answer and decide | PostgreSQL-backed question answer and approval denial, recorded receipt focus and list reconciliation | Synthetic runtime requests; this does not establish delivery to a native process |
| Inspect artifacts | Scope artifact list, originating run and service, explicit content read, verified fictional content, download availability and hide action | Review object storage is in memory; this is not production storage qualification |
| Manage lifecycle | Named-route withdrawal, blocked retirement while routes or runs remain, successful retirement after those prerequisites, return to the correct resource tab | Development authority and reference resources only |
| Discover related work | Explicit workspace relationship, service discovery, service creation, return path, server search beyond the initial 50 rows and empty search | Workspace files, notebooks and saved queries remain separate missing capabilities |
| Visual and keyboard behavior | Both themes, long names, visible focus, dialog dismissal and focus return, narrow 320-pixel workbench layout and keyboard search | Manual checks and automated accessibility tests do not establish complete WCAG conformance or representative-user acceptance |

Repository CI passed for the interaction changes in PR #415 and service search
in PR #416. The replacement has automated coverage for retained forms and exact
requests across resource navigation, denied and stale reads, scope and identity
replacement, request expiry, uncertain submissions, cancellation, direct links,
and publication, routing and retirement preconditions. During classic removal,
those cases move to the workbench views rather than retaining a second page
composition solely for tests. Resource-selection checks remain independently
tested and continue to gate publication, routing and invocation commands.

The retired presentation had its own navigation and stacked inspection pages.
The replacement uses one persistent shell, five service views and five run views.
The same public contracts, action permissions, content disclosure controls,
idempotency keys and command implementations remain in effect.

Release validation still requires representative-user task testing, complete
accessibility evaluation and the relevant real runtime evidence. Catalog, SQL,
notebook, job, persistent-assistant and administration journeys are not established
by this record. Governed source execution and workspace retention prerequisites
remain as described in the delivery ledger and canonical platform documents.
