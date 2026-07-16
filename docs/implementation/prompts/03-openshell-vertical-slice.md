# Codex prompt 03 — local OpenShell vertical slice

## Goal

Run one pinned real agent runtime in a local OpenShell sandbox behind DataGround's execution-provider and runtime-adapter boundaries, without exposing infrastructure or raw provider credentials.

## Context

Read ADR-015, ADR-018–024, ADR-032, the OpenShell sections of the specification, `docs/implementation/build-readiness.md` and `docs/implementation/verification-and-release-gates.md`. Confirm the pinned OpenShell release and local topology exist before editing.

## Work

1. Capture the exact gateway/supervisor/driver versions, digests, schemas and supported capabilities in a development certification manifest.
2. Implement or complete the `ExecutionProvider` adapter for register/select gateway, reserve placement, create/observe/route/log/export/terminate and drain/loss behavior.
3. Use an externally supplied development enforcement bundle or deny-all fixture with immutable hash/provenance. If none exists, stop and report the blocker.
4. Integrate the selected first real runtime through its confirmed native southbound interface.
5. Normalize events and capabilities into the existing contract without making the native protocol public.
6. Persist outputs/artifacts before teardown and correlate gateway, sandbox and upstream IDs only in protected internal state.
7. Test idempotency across timeouts/lost acknowledgements and verify cleanup/orphan reconciliation.
8. Prove callers never receive gateway URLs, sandbox ports, cluster credentials or native runtime endpoints.
9. Prove process inspection cannot reveal a provider key or refresh token; use OpenShell mediation or a credential-holding bridge.

## Constraints

- Rosetta unavailable means fail closed. Do not write a translator.
- A manual development policy is never production-certifiable.
- Do not expose experimental WebSockets or raw service forwarding publicly.
- Do not add special public API fields for this runtime.

## Done when

- The real runtime completes the same publish/invoke/event/cancel/artifact flow as the fake.
- Provider and execution conformance tests pass with recorded evidence.
- Restart, timeout, lost acknowledgement, drain and teardown failures are tested.
- Security evidence covers endpoint and credential non-exposure.
- Five review passes cover factual and contract accuracy, scope and instruction precedence, security and isolation, failure recovery and operability, then maintainability and regression. Retain only improvements.
- Return pinned inputs, commands/results, limitations and the next safe prompt.
