# Codex prompt 06 — release hardening

## Goal

Turn the completed first vertical slice into a reproducible, operable release candidate for Developer, Team and Production profiles.

## Context

Read `docs/implementation/`, specification Sections 15–20, all relevant ADR exit gates, release manifests and runbooks. Preserve declared capability differences between profiles.

## Work

1. Finalize signed release manifest, images/digests, SBOM, provenance, schemas, database version and runtime/gateway matrices.
2. Validate clean install, upgrade, prior-matrix rollback and teardown in local Docker/Podman, local Kubernetes and the production reference profile.
3. Test backup/restore, gateway drain/loss, stuck-state repair, orphan cleanup, callback delivery where present and incident evidence collection.
4. Run isolation, raw-credential, authorization, supply-chain, deletion, operator-content and endpoint-exposure tests.
5. Run load, soak, failure and recovery tests; publish measured capacity envelopes and overload behavior without invented targets.
6. Validate telemetry/audit correlation, redaction, retention and dashboard/runbook usefulness.
7. Run the complete accessibility journey matrix.
8. Disable any Rosetta-dependent capability unless its external contract and conformance suite are complete.
9. Produce release notes, known limitations, unsupported combinations and operator prerequisites.

## Constraints

- Do not waive security boundaries, durable-state recovery, signatures or credential non-exposure.
- Do not claim production capability from local-only evidence.
- Do not add Temporal, broad OpenAI compatibility, Databricks migration or unrelated product scope.

## Done when

- Every release-candidate gate has stored evidence or an explicit release-blocking failure.
- Restore and rollback are demonstrated, not merely documented.
- Capacity and SLO statements are measured and environment-qualified.
- Five review passes cover factual and contract accuracy, scope and instruction precedence, security and isolation, failure recovery and operability, then maintainability and regression. Retain only improvements.
- Return a go/no-go recommendation with blockers, evidence links and follow-up items.
