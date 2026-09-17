# Shared runtime adapter contract tests

ADR-004 requires runtime families to use one worker-facing contract and a shared test kit. `internal/runtime.Adapter` defines the session entry point used by the invocation worker. `internal/runtime/runtimetest` checks that contract with deterministic fixtures. Run the kit and the current Codex fixtures with:

```sh
go test ./internal/runtime/...
```

The repository Go tests run both the deterministic test model and the Codex app-server fixture through the same assertions. The test model is separate from the product reference engine. It proves that the kit can run without a native runtime; it does not prove sandbox enforcement. The Codex fixture verifies native request targets, approval decisions, and question answers in addition to the shared assertions.

## Adding an adapter

Call `runtimetest.Run` from the adapter's tests with a factory and an explicit `Features` value. The factory creates a fresh adapter and native session for each scenario. It returns an idempotent release function and a verification function. Emit the native start notification during `Start`, then wait for release before sending the scenario's remaining frames. The verification function must check native effects and honor its context. Cleanup must unblock the fixture even if an assertion fails.

The common cases require ordered text, tool, process, and file activity; success and failure completion; explicit interruption; malformed protocol rejection; scope mismatch rejection; safe process failure; single-use session ownership; and invalid start rejection before native admission. An adapter must reject a second invocation even after its first turn has completed. Cancelling an observer's wait must not cancel the turn. Repeated waits preserve the terminal result, and repeated close calls are safe. The suite permits either a closed event channel or an open idle channel after completion. Fixture events must be valid JSON, fit within the kit's 128 KiB envelope ceiling, and exclude the private marker. That fixture ceiling does not replace the production limits on each payload type.

Set `Features.Approvals` only when this adapter configuration supports explicit decisions. Its fixtures must issue one process-execution approval, retain it until a platform decision, and check the exact native allow or deny response. Invalid decisions and duplicate resolution must fail. Set `Features.Questions` only when this configuration supports interactive questions. Its fixture must issue one single-choice prompt and one free-text prompt, then verify delivery of the original selected label and explicit text. The suite changes the displayed choice label to test that the adapter keeps an independent frozen mapping. Missing answers and duplicate answers must fail. Both interaction handles must lose authority when consumed. A feature set to false requires the corresponding interactive start to fail before a turn is admitted.

Place `runtimetest.NativeCanary` in private native identifiers, activity commands and paths, failure details, and process errors. Use `runtimetest.OutputText` only for the intended public text. A fixture must translate the documented scenario into its native protocol; it must not replace the adapter with precomputed normalized events. The deterministic model inside the kit is the sole test-only exception. Additional native tests remain necessary for protocol limits, duplicate frames, blocked I/O, pending interaction cancellation and expiry, and other family-specific behavior.

## Evidence limits

This kit covers an internal invocation boundary. It does not certify a live runtime, provider, deployment, or capability manifest. It does not yet test usage normalization, resume, steering, runtime artifact events, artifact storage, structured output enforcement, or Hermes profile operations. Those boundaries need their own tests and accepted evidence before a release claim.

The Codex fixture enables the internal experimental question path to test its semantics. This does not change the checked development profile's unsupported question classification or enable questions in ordinary governed execution. Claude Code, OpenCode, and Hermes adapters are still absent. Their required family capabilities and live differential tests remain release work. The existing OpenShell runtime evidence and certification checks remain separate gates; passing this kit supplies no accepted runtime-conformance record or v1.0 release acceptance.
