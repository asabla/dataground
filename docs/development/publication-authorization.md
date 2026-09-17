# Governed publication authorization

The internal publication authorizer supplies the policy boundary needed before the public API can accept governed publication. It uses the existing exact-revision policy installation, entity refresh, and irreversible withdrawal mechanisms. Public publication remains reference-only. The current strict development publication and invocation commands still require their version 3 invocation policy; they do not select this new authorizer. This contract does not establish public publication support or release acceptance.

A publication-capable bundle uses `dataground.invocation-authorization-policy/v5`. It retains version 4 invocation, approval and question actions, and adds `publish` with a `DataGround::Actor` principal and `DataGround::ServiceRevision` resource. The distinct digest domain binds the canonical schema, policy bytes and complete actor/role entity snapshot. An older version 1–4 bundle is always denied publication authority, even if its Cedar policy has a wildcard action or resource. Selecting version 5 is an explicit installation decision, not an automatic capability upgrade.

The publication context contains the isolation domain, service, revision, operation and correlation identities; `phase` (`entry` or `effect`); expected draft version; reviewed plan digest; verification-configuration digest; and fencing token. Entry uses token zero. Effect requires the positive current lease token. No bearer token, provider credential, policy payload, native endpoint, prompt or artifact content enters this context. The caller must derive actor identity from authenticated or accepted durable state. This authorizer does not authenticate a public request or accept an actor supplied by a public body.

For example, a reviewed policy can permit a publisher role to publish one revision at either boundary:

```cedar
permit (
  principal in DataGround::Role::"publisher",
  action == DataGround::Action::"publish",
  resource == DataGround::ServiceRevision::"rev_00000000000000000001"
)
when {
  context.isolationDomainID == "iso_00000000000000000001" &&
  context.expectedVersion == 1 &&
  (context.phase == "entry" || context.phase == "effect")
};
```

Restrict the service, reviewed digests or phase further when required. Keep invocation permissions explicit in the same reviewed bundle; the example grants publication only. Install the bundle with the existing `go run ./cmd/dataground-policy-install` scope, policy file, canonical entity file, actor, reason and correlation flags plus `--publication-capable`. This flag is mutually exclusive with `--approval-capable` and `--question-capable`. Use a new revision for a new policy contract: an existing immutable version 3 installation cannot be replaced in place.

`PublicationAuthorizer.AuthorizePublication` resolves the current unwithdrawn bundle for the exact isolation domain, service and revision and requires its effective digest to equal the caller's reviewed pin. There is no latest-revision or wildcard scope fallback. Each call resolves current state. An entity activation changes the effective digest; an old reviewed pin then fails before evaluation. A caller with a newly reviewed digest is evaluated against the new membership. Policy withdrawal prevents later resolution. A lifecycle caller must hold the policy and operation fencing boundary across the effect authorization and publication commit; a successful policy decision is not a lease or permission to bypass runtime verification.

Every completed evaluation records allowed, denied or unavailable in the separate append-only `publication_authorization_decisions` stream before returning the result. Source lookup failure, invalid scope, mismatched reviewed policy digest and cancellation are not mislabeled as completed evaluations. A failed decision-audit write withholds an otherwise allowed result. Records retain the complete bounded request context, policy contract, policy-set identity, effective digest, outcome and database timestamp. They do not fit the existing API or invocation decision export contracts and are not included in those streams.

Entry audit verifies the exact service/revision scope without requiring the operation to exist yet. Effect audit additionally requires a validating version 3 queued publication with its current unexpired lease and deadline, effective actor and correlation, and identical immutable reviewed inputs. Its independent audit transaction uses snapshot reads so it can complete while the lifecycle transaction holds the operation lock. It does not take a foreign-key lock that would deadlock with the caller.

Migration 58 admits version 5 policy and question-decision provenance and adds the publication decision stream. Update and deletion of decision rows are rejected. Downgrade refuses to discard any publication decision, version 5 policy, or version 5 question decision, including historical evidence. Focused PostgreSQL tests cover phase binding, substituted scope and inputs, stale fencing tokens, audit failure, entity refresh, withdrawal, lock interaction and retention. These are internal authorization tests, not evidence of a completed public publication journey.
