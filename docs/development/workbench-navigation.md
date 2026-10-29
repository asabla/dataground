# Workbench navigation

The connection boundary in `App.tsx` owns the memory-only development
credential. `WorkbenchShell` owns the persistent application frame.
`DevelopmentWorkbench` resets the workflow session when the client identity or
isolation domain changes. The workflow session retains the existing command
implementations; the navigation hook owns browser location changes.

The initial supported locations are:

| Location after `#/scopes/{isolationDomainId}/agents` | View |
| --- | --- |
| No suffix | Service directory |
| `/new` | Create service |
| `/runs` | Interaction entry without a selected service |
| `/{serviceId}/overview` | Service summary and next action |
| `/{serviceId}/build` | Revision authoring and publication |
| `/{serviceId}/versions` | Revision history and retirement |
| `/{serviceId}/deployments` | Routes, assignment and withdrawal |
| `/{serviceId}/runs` | Selected service invocation history |

A direct service location reads that exact service through the authorized public
API. It does not scan service pages or use the directory as evidence that another
resource is readable. Invalid paths, unknown views and another isolation scope
show an unavailable location. A late read cannot restore a service after the
user leaves that location.

Browser back and forward select the recorded view. Refresh retains resource
identity in the URL and requires a new connection because credentials are not
persisted. Forms, prompts, answers, result contents, directory searches and
idempotency keys stay out of URLs and browser storage. Refresh does not retain
unsent form content.

Each opened service has a separate in-memory workflow session. The open-work
navigation returns to that retained context, including unfinished forms and
exact-request retry state. The creation draft also has a stable session; opening
its confirmed service transfers the resource identity without replacing that
session. Selecting another service no longer discards the previous service's
commands. Only services the user opens acquire sessions; there is no directory
fan-out to load or monitor every service.

Hidden contexts remain mounted, but are removed from keyboard and accessibility
navigation. Heading IDs are unique across retained contexts, and audit focus
returns to the correct resource. Identity or scope changes unmount all sessions
and invalidate pending reads. Refresh also clears all memory-only sessions.
Explicitly starting another revision still replaces the revision context within
that service. Revision-specific command retention and revision/run/artifact deep links remain
subsequent migration work. The production screen composition is still in migration.

The service workbench uses Overview, Build, Versions, Deployments and Runs as
separate views. Shared React Aria tabs mount panels on first visit and retain them
thereafter. Inactive panels are inert and hidden. Tab URLs contain only closed
view names and resource identities. Read failures stay visible outside the tabs.

The connection form temporarily offers Workbench and Classic presentations for
validation. They use the same command implementations. Selection is fixed for the
connection; changing a presentation prop resets the session just as an identity
change does. Do not switch presentation inside an active command session.

The navigation parser has closed-path tests. Browser lifecycle tests cover
retained exact-request retries, direct reads, unavailable services, late reads
connection replacement, independent service drafts and exact retries across services. Manual built-in Browser checks use the actual
production React application and an isolated loopback reference API with a
fictional identity: service creation, draft retention, back navigation, refresh,
reconnection and exact service loading. This evidence does not establish governed
runtime or production identity acceptance.

## Run inspection

The workbench presentation keeps the run outcome, loading or degraded state,
failures and cancellation controls above Activity, Requests, Output, Files and
Audit tabs. Resource identities and operation bookkeeping are secondary details.
Created time is labeled as record creation, not execution start. Desired and
observed operation states remain distinct. A selected question, approval or
artifact stays beside the surrounding run view, and returns below it on narrow
screens. Closing inspection returns focus to the opener when it remains usable,
or to run navigation when the opener is hidden or gone.

The existing invocation controller owns cancellation and exact-request recovery.
Result, artifact content and audit disclosure retain their explicit access
controls. Panels load on first visit, then stay mounted. Merely choosing Output
does not request result content. Files shows a bounded set of references from the
authorized invocation read; metadata inspection and content reads remain separate.
The reference API review covers successful creation, publication, routing,
invocation and explicit result display. It does not prove native execution.

Version, route and run browsers use the shared resource table. Sorting and
filters apply to loaded pages only; each browser states that boundary. Pagination
continues through the original authorized endpoints. A route shows a revision
number only when it is present in the loaded, validated revision history;
otherwise its exact reference remains available in secondary details. Refresh
and error states disable retained actions. The table refresh dependency contract
ensures those controls do not retain an earlier enabled state.
