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
