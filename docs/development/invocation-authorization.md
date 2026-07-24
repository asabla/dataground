# Invocation authorization policy boundary

Governed invocation admission, runtime execution, and cancellation share one provider-independent authorization request. A deployment can bind that request to an immutable policy bundle through the opt-in policy-bound decision adapter in `internal/reconcile`.

The policy source resolves by exact isolation domain, service, and revision. A returned bundle must use the closed `dataground.invocation-authorization-policy/v1` contract, repeat that complete scope, provide a portable policy-set identifier, keep both Cedar schema and policy bytes within their independent bounds, and match the length-delimited SHA-256 digest of those bytes. Invalid or scope-drifted bundles fail before evaluation. Policy bytes are copied at the boundary so neither the source nor evaluator can mutate another component's state.

The evaluator is an explicit deployment dependency. It receives the verified immutable bundle and the already normalized authorization request, including durable actor, correlation, operation, invocation, and runtime context. Only the stable authorization-denied outcome is translated into the existing phase-specific denial errors. Other source and evaluator failures are reported as policy unavailability without exposing backend diagnostics; cancellation and deadline outcomes remain distinguishable.

This boundary does not implement a Cedar parser or evaluator, policy persistence, policy authoring, identity resolution, a default policy, or default-worker composition. It also does not authorize OpenShell calls, configure credentials, or certify live execution. Those capabilities remain blocked until their concrete implementations and conformance evidence are added.
