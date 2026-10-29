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
| `/{serviceId}/runs/{invocationId}/{view}` | Exact run with Activity, Requests, Output, Files or Audit selected |

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
that service. Revision-specific command retention and revision/artifact deep links remain
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

Activity keeps the journal's confirmed sequence order in a compact table. It does
not infer task dependencies, durations or execution graphs. Selecting an event
opens its bounded display summary and envelope details beside the run. Runtime
completion remains distinct from the platform outcome. Question and artifact
references keep their existing independent inspection controls. Opening an
activity detail does not destroy an unfinished question or approval; closing it
restores the prior inspection. Connection and invocation changes discard the
selected activity snapshot.

The shell provides System, Light and Dark appearance choices through the existing
semantic tokens. Appearance is held in memory for the current connection. Changing
it does not replace open workflow sessions or clear unfinished forms. Disconnecting
resets the choice. The header reflows with the rest of the workbench on narrow
screens.

Opened history runs retain independent inspection sessions in the workbench
presentation. Returning to the list, refreshing its page, or opening another run
does not discard an unfinished answer, an uncertain command retry, or the selected
inspection view. Inactive sessions are hidden and inert. A changed client, service
or isolation scope, or a denied history refresh removes those sessions. Reopening
requires a current successful list row; the retained session does not grant access.
Existing refresh, expiry and command authorization checks still apply.

Exact run URLs use the closed view names `activity`, `requests`, `output`, `files`
and `audit`. Direct links use the authorized invocation read and verify its service
relationship before mounting inspection. They do not scan history pages. A denied
or mismatched read shows an unavailable run; late reads cannot reopen a departed
location. An authorized exact run read is independent of list permission. Back,
forward and reconnect restore the selected run view. Output links retain explicit
result disclosure. Newly accepted invocations enter the same run navigation.

The workbench command presentation uses the shared page surface for service
creation, revision definition, publication, routing and invocation composition.
Runtime/capabilities, input and output fields have distinct sections. IDs and
receipt bookkeeping use secondary disclosures; errors, uncertain outcomes,
required capabilities and the next action remain visible. Publication and routing
confirmations restate their exact revision and isolation scope. The same workflow
controllers and request objects serve both this presentation and the temporary
classic cards. Command transitions restore focus when their initiating control is
removed and no other control has focus.

Secondary service and command identifiers provide explicit copy controls. They
report success only after the clipboard write completes and show a safe manual
copy alternative on failure. Changing the value discards old feedback; the
control does not copy on mount or store its value in browser persistence.

Revision drafting has a stable slot in Build. Opening a saved version, discovering
a route, or returning through New revision keeps the unfinished draft and its
exact recovery request. A fresh draft replaces it only after creation was
confirmed. Hidden drafts are inert; changing connection identity or scope still
removes the service session. The temporary classic presentation retains its
existing navigation behavior.

Publication, routing and invocation composition also have fixed view locations.
Each opened revision or route context retains its existing command controller and
a snapshot of the exact resource preconditions. Selecting another context or
waiting for discovery hides that controller instead of replacing it. Returning
to the same observed context restores the original retry request. Confirmed new
resource versions create separate command contexts. Entry scope checks and the
existing server authorization, version and idempotency checks remain required;
retention does not grant access or change a route's API semantics.

Requests uses compact question and approval tables with loaded-page state filters,
received-time sorting and declared expiry. Row actions open the existing focused
inspection; question content and approval effects keep their separate access and
confirmation boundaries. Missing expiry in an older approval contract is shown
as not supplied. Refresh, pagination, denial and stale-control guards are shared
with the temporary classic lists.

Artifact inspection appends `/artifacts/{artifactId}` to an exact run-view URL.
The run read verifies the service relationship, and the artifact metadata read
independently verifies its invocation and isolation scope. Closing inspection
removes the suffix; browser back and refresh restore metadata in the same run
view. A link never requests file contents. Content disclosure, authorization,
digest verification and stale-response fences remain in the existing artifact
workflow. A changed artifact link replaces a prior event selection so it cannot
hide the requested artifact.

Artifact inspection leads with the output name, availability, type, size and
sensitivity. Resource identities and integrity/provenance fields have secondary
disclosures with explicit copy controls. Errors and sensitive-content notices
stay visible. The metadata update time is labelled Record updated; it does not
indicate execution readiness. The compact presentation uses the same content
authorization and verification controller as the temporary classic card.

A selected revision can be restored through
`#/scopes/{isolationDomainId}/agents/{serviceId}/{serviceTab}/revisions/{revisionId}`.
Build, Overview, Versions and Deployments preserve this exact context. The
existing revision read validates its service and isolation relationship; a late
list response cannot select another revision. New revision returns to the
retained draft without a revision suffix. Runs use their separate invocation
locations. These links contain identities only and do not persist forms or
command attempts across refresh.

Run inspection uses the shared bounded split view. The separator can be dragged
or adjusted with the keyboard, and both panes remain available in a stacked
layout when their container is narrow. Resizing and opening or closing the
inspector do not remount the run view or clear its unfinished answers.

The Files view loads authorized artifact metadata in pages of 50, with explicit
load-more and refresh actions. Its compact table compares names, states, formats,
sizes, sensitivity and creation times. Search, state filters and sorting apply to
the loaded rows only. Listing does not grant access to exact metadata or content;
inspection uses the existing independent reads. Invalid or repeated pages and
failed discovery clear the rows, while identity changes reject late responses.

Question and approval inspectors share the run surface. Request identities have
secondary copy controls; deadlines, recorded decisions, required actions and
uncertain outcomes remain visible. Question blockers precede the answer fields.
The compact presentation passes through the same command, expiry, authority and
exact-request retry controllers as the temporary classic presentation.

Withdrawal and retirement keep their explicit confirmation and recovery actions
in focused lifecycle sections. Service names and revision numbers lead the view;
resource and record identifiers have secondary copy controls. Consequences,
preconditions, pending commands and errors remain visible. A terminal retirement
failure also blocks a stale confirm handler from sending the command again.

The primary Runs destination lists executions across services through the separate
`listScopeInvocations` authority. Its state filter applies at the server before
pagination; text search and sorting apply to loaded summaries. Service names,
routes, observed state and record creation or completion times support comparison.
No execution start time or duration is inferred from metadata. Opening a row uses
the exact service and run locations with their independent authorization checks.
The list, filters and loaded pages remain in memory across supported navigation,
including a denied detail read, and are cleared with the connection identity.
The workbench does not request each service to simulate a global list.

The primary Artifacts destination uses the separate scope-level metadata query,
with server-side state filters and loaded-name/service search. Source service,
format, size, sensitivity and retention state support comparison. Inspect opens
the artifact in its exact run Files view, using the normal independent service,
run and artifact reads. The global list and its filters remain in memory on
return. Only resource identities enter the URL; content access remains explicit.

Run and artifact metadata browsers share the scoped discovery controller. The
reader identity includes the connection, scope and server filters. New reads
invalidate prior row actions immediately; stale responses, repeated pages and
closed views cannot restore access to those rows. The controller retains no
credentials or content and does not replace API authorization.

Workbench publication uses the shared React Aria dialog for its bounded decision.
The dialog retains the local theme, contains keyboard focus and returns focus on
close. The non-mutating action receives initial focus. A pending submission blocks
Escape and dismissal; its existing exact-request recovery remains in the page
when the outcome is uncertain. Retained publication sessions receive an explicit
visibility boundary, so a hidden service or tab cannot leave a modal open. The
classic presentation retains its inline confirmation and shares the same command.

Home has a stable scope URL and retains up to twelve recently opened service
sessions in memory. This is connection-local work history, not a workspace,
ownership record or saved-resource service. Refresh or identity changes clear it.
Its attention preview uses one bounded scope run query for the selected waiting,
failed or unknown state, shows up to eight summaries and links to exact inspection.
It reports query denial separately from an empty list and does not load prompts,
results or files. Runs remains the full paginated browser. Saved resources and
analyst recents require their future durable contracts.

Version rows keep Open as the primary action. Their shared React Aria action menu
contains audit inspection and, for published revisions, retirement review. The
menu retains its local theme and visibility boundary, skips disabled items and
returns focus on dismissal. It invokes the same guarded callbacks; it never
executes retirement directly. Opening audit moves focus to the inspection region.

Workspace now uses real workspace identities and explicit service relationships.
Its scope list and exact detail routes are `#/scopes/{scope}/workspaces` and
`#/scopes/{scope}/workspaces/{workspaceId}`. Lists use bounded API pages; search
filters only loaded records. Exact workspace and service reads keep independent
authorization. A service's workspace ID supplies a direct return path without
inferring ownership or execution access.

Workspace and service creation forms remain in the connected session when hidden
or when the user navigates elsewhere. Uncertain responses retain the original
request fields and idempotency key. A late completion does not redirect the user
from a different area. Identity or scope changes clear these forms. Workspace
creation and metadata reads work against the reference and durable API profiles;
this area does not yet contain notebook documents, files, saved queries or a
resource tree. Those destinations require their own public contracts.

Successful service, revision and route commands return lost focus to their receipt
when their submit control disappears. Hidden sessions and other focused controls
are not interrupted. A completed route assignment also refreshes the authorized
route list, preserves its loaded-name filter and rejects an older page that
finishes after the refresh. The list still requires its own discovery permission.

Route assignment uses the shared dialog in the Workbench presentation. It names
its route, target revision and isolation scope. Initial focus selects the action
that keeps the current route; Escape returns to the review control. A pending
submission blocks dismissal. Retained inactive route sessions do not open a
modal, and uncertain submissions keep their original retry command outside it.

Accepted invocation receipts use the same lost-focus recovery as other command
results. Acceptance refreshes the selected service's authorized run history without
changing the current run location or clearing its filter. A late earlier history
page cannot replace the refreshed list. Result and request contents still require
their own inspection reads.

A successfully assigned or already observed route offers **Create another route**.
This opens a blank name for the same published revision and uses the create-only
zero-version precondition. It does not move the previous alias. A pending or
uncertain request cannot be replaced by that action; its exact retry remains.
Connection or revision changes discard the new-route form and invalidate late
responses from the previous command session.

An accepted run offers **Start another run**. It keeps the selected route, clears
the previous input and uses a new request identifier on submission. Pending and
uncertain submissions cannot be replaced this way; they retain their exact retry.
Replacing the connection clears the composer and prevents an earlier response
from publishing a receipt or refreshing the new connection's run history.

Recorded answers and approval decisions restore lost focus to their request
heading and refresh the corresponding authorized request list. The inspection
stays open and list filters remain selected. A list response from before that
refresh cannot replace the newer state. Recording a response does not imply
confirmed delivery to the runtime.
