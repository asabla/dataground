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
| `/{serviceId}/build` | Selected service workflow |
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

Moving between the directory, service workflow and interactions retains the
active service's command component and unfinished forms. The command region has
one stable mount; hidden views are removed from keyboard and accessibility
navigation. Identity or scope changes unmount the whole session and invalidate
pending reads. Explicitly starting another revision or selecting another service
still replaces the active context. Per-resource command sessions, all five
service tabs, and revision/run/artifact deep links remain subsequent migration
work. The production screen composition is still in migration.

The navigation parser has closed-path tests. Browser lifecycle tests cover
retained exact-request retries, direct reads, unavailable services, late reads
and connection replacement. Manual built-in Browser checks use the actual
production React application and an isolated loopback reference API with a
fictional identity: service creation, draft retention, back navigation, refresh,
reconnection and exact service loading. This evidence does not establish governed
runtime or production identity acceptance.
