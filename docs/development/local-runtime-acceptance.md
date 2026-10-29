# Local candidate runtime acceptance

The standalone `openshell:local:acceptance` command prepares and verifies an explicitly scoped, signed acceptance of a published candidate and its local runtime diagnostic. Model credentials stay with the local conformance runner. This boundary implements the experimental service-revision allowance in ADR-022: its profile is `openshell-codex-candidate-development/v1`, its deployment scope is `loopback-development-only`, and `certificationEligible` remains false. It does not provide release certification. The [governed worker](governed-worker.md) can explicitly select this experimental profile with independent acceptance pins; running this standalone command alone does not activate it. The existing CI evidence and stock runtime-certification contracts remain unchanged.

The [closed versioned schema](../../deploy/openshell/local-runtime-acceptance.schema.json) defines the acceptance envelope, statement and trust profile. Documents use UTF-8 JSON with recursively sorted object keys, compact separators and one trailing newline. Duplicate keys, unknown properties and alternative serializations fail verification. The diagnostic and image artifacts retain their exact original bytes.

An operator independently pins the trust profile's SHA-256. That profile authorizes one Ed25519 public key and reviewer for one isolation domain, service and revision, with explicit validity boundaries. It also pins the Sigstore roots acquired through the authenticated process described in [candidate provenance verification](codex-compatibility.md). Supplying a key or roots alongside an untrusted acceptance is insufficient. Trust rotation requires changing the deployment's independent trust pin; the caller also supplies a minimum generation and explicitly rejected acceptance IDs. This command does not maintain a durable generation or revocation ledger.

The statement binds the exact target, generation, acceptance ID, reviewer, reason, model, tested DataGround source revision, checked development-profile digest, local diagnostic and image configuration digests, raw signature bundle, published image digest, publication source/run/attempt/architecture and trust profile. The published build source and local diagnostic source are distinct fields because a later conformance runner can test an earlier immutable image. Neither field attests the source revision of a future worker binary. The local image ID must match either the signed manifest digest or its configuration digest, and preparation additionally requires Docker to resolve the published image to that exact locally observed ID.

Acceptance expires at most 24 hours after the diagnostic finishes and must fit entirely within the signing trust's validity window. Issuance must follow the completed local run, and a clock difference of at most five minutes is permitted. Validity is checked before and after external verification. Every diagnostic case, cleanup binding, synthetic credential check, model and checked candidate policy must pass; a signature cannot relax them.

## Prepare and sign

Retain these owner-only regular files in one private evidence directory: `diagnostic.json`, `manifest.json`, `image-config.json`, `bundle.jsonl` and `trusted-root.jsonl`. Reads are bounded, reject leaf symlinks and freeze the same bytes used by all verification layers. The diagnostic is limited to 64 KiB; image manifest, configuration and roots to 1 MiB each; and the Sigstore bundle to 4 MiB. Statement, envelope and trust files are each limited to 64 KiB.

Create a canonical statement and trust profile using the schema, with the independently reviewed digests and scope. With GitHub CLI 2.98.0 and Docker available, prepare the signing message:

```sh
pnpm openshell:local:acceptance prepare \
  /absolute/private/statement.json /absolute/private/trust.json \
  /absolute/private/evidence "$REVIEWED_TRUST_SHA256" \
  "$TESTED_SOURCE_REVISION" /absolute/private/signing-message
```

Preparation independently verifies the offline GitHub/Sigstore image proof, including its raw configuration, then reruns online publication verification against the exact completed workflow attempt and both successful jobs. It pulls the immutable image and compares its local identity with the diagnostic. It writes a new owner-only signing-message file only after those checks succeed, and refuses to overwrite an existing file. This stage may use local GitHub registry access; it acquires no model credentials.

Use the authorized external Ed25519 signer to sign the exact message bytes. The message consists of `DataGround local candidate runtime acceptance v1`, a newline, and the canonical statement bytes. The trust profile contains the canonical base64 encoding of the 32-byte public key; the envelope signature contains the canonical base64 encoding of the 64-byte signature and the same key ID. Assemble the canonical envelope with the unchanged statement. The command neither acquires a signing key nor creates a signature.

The reviewer signature vouches for the local observations and for successful workflow completion checked during preparation. GitHub's image attestation verifies the build provenance; it does not sign the local run or guarantee that steps after image signing succeeded. The reviewer must inspect the exact statement and retain this distinction when signing.

## Verify an exact acceptance

Pin the envelope digest independently and supply the expected service scope, tested source, minimum generation and any rejected acceptance IDs:

```sh
pnpm openshell:local:acceptance verify \
  /absolute/private/envelope.json /absolute/private/trust.json \
  /absolute/private/evidence "$REVIEWED_TRUST_SHA256" \
  "$TESTED_SOURCE_REVISION" "$ACCEPTED_ENVELOPE_SHA256" \
  "$ISOLATION_DOMAIN_ID" "$SERVICE_ID" "$REVISION_ID" 1
```

To reject prior acceptances, append one comma-separated argument such as `rtlocal_0123456789abcdefghij,rtlocal_abcdefghij0123456789`. Missing or invalid scope, stale generation, an explicitly rejected ID, expiry, altered evidence, mismatched policy/model or an invalid reviewer/image signature fails closed. Verification reruns the offline image proof in the isolated, credential-free child environment. It emits only the accepted scope, image, model, generation, profile, expiry and explicit development limitations. It does not requery GitHub: the trusted reviewer's signed completion assertion supplies that part of the acceptance.

The command is a verification boundary, not a deployment controller. The explicit governed composition pins the returned image, model and exact candidate enforcement policy and checks acceptance again at consequential effects and renewal. Its operator owns the configured generation/revocation state and restarts the worker to change those pins. Reference mode remains the default, and this standalone command cannot make an experimental revision production-ready.

## Strict runtime acceptance

The [version 2 schema](../../deploy/openshell/local-strict-runtime-acceptance.schema.json) adds the `openshell-codex-strict-candidate-development/v1` profile. It requires a v5 diagnostic from the strict Rosetta policy and the patched supervisor. Both the Codex image and the supervisor image must have an exact, verified ARM64 publication. The version 1 contract still accepts only its original v3 diagnostic and candidate policy.

Use the same `prepare` and `verify` commands. Set the envelope, statement, and trust contracts to their `/v2` versions. The trust document must also specify the strict profile. This requires a new independent trust pin; a version 1 trust document cannot authorize the strict profile. The signature message starts with `DataGround local candidate runtime acceptance v2` followed by a newline and the canonical statement bytes. A version 1 signature cannot authorize a version 2 statement.

The statement retains the Codex publication fields and adds a required `supervisor` object with `publication`, `localImageId`, `configSHA256`, and `bundleSHA256`. These fields have the same exact digest and invocation rules as the Codex fields. Add `supervisor-manifest.json`, `supervisor-image-config.json`, and `supervisor-bundle.jsonl` to the private evidence directory. The manifest and configuration limits are 1 MiB each; the bundle limit is 4 MiB. Both image proofs use the independently pinned Sigstore roots in the trust document.

Preparation verifies both image signatures and both completed publication attempts. It then checks that each pulled image matches the identity recorded by the same local diagnostic. Verification checks both offline image proofs, the exact strict policy and gateway configuration, all twelve diagnostic cases, cleanup, service scope, model, generation, rejection list, and validity period. Failure of either image proof prevents acceptance. Expiry is checked again after external verification.

The strict receipt adds the published supervisor image, its exact local image ID, the gateway configuration digest, and the enforcement-policy digest. These values come from the verified evidence. This command does not inspect a running deployment. The governed worker can explicitly select the strict profile with independent deployment pins and the read-only running-topology observer described in [governed worker configuration](governed-worker.md#use-strict-local-acceptance-with-a-pinned-deployment). It accepts this profile only on Linux ARM64 and rechecks both the v2 acceptance and that exact deployment at its existing verification boundaries. Neither acceptance version supplies production certification.

## Source-capable diagnostic prerequisite

The local conformance command accepts `--workspace-source` only with a candidate image, the strict Rosetta policy, and an exact supervisor candidate. This selects the closed [v6 diagnostic](../../deploy/openshell/runtime-workspace-diagnostic.schema.json). The existing v1 and v2 acceptance contracts reject v6. The v3 acceptance below verifies it; explicit worker composition remains required before source-enabled governed execution can use this evidence.

Before the twelve model-backed cases, the launcher uses the same provider-bound sandbox to reject a cross-domain transfer, install a synthetic binary file, executable, and contained symlink, repeat exact read-back, reject a different archive, and confirm that the original tree remains intact. A separate bounded native session then reads the exact bytes and private modes and runs the installed executable under the read-only native policy. That session must close before the model cases start. The source probe has a one-minute deadline and bounded protocol and diagnostic streams. Any failed check or cleanup withholds the diagnostic.

For a strict local conformance invocation, add `--workspace-source` to the existing `--local-diagnostic --candidate-image sha256:<id> --policy-profile rosetta-development/v1 --supervisor-candidate-image sha256:<id>` selection. Keep the required explicit model, source revision, private workspace, and owner-supplied credential bundle. This command does not acquire model credentials. The source evidence records only the fixed synthetic archive digest, run binding, ordered check names, and preparation window. It contains no user source. The verifier requires that preparation finish before the same run's model checks begin, and it retains all strict image, policy, topology, and cleanup checks.

The provider-bound compatibility tests run this same source probe with synthetic provider credentials. Passing those tests verifies the probe's live transport and filesystem behavior; it does not establish a completed model-backed v6 diagnostic or signed runtime acceptance.

## Source-capable runtime acceptance

The [version 3 schema](../../deploy/openshell/local-workspace-runtime-acceptance.schema.json) defines `openshell-codex-workspace-candidate-development/v1`. It retains the strict ARM64 runtime and supervisor proof requirements, and requires the exact v6 diagnostic with its source preparation and native read-back checks. The signed statement also requires `workspaceSourceContract: "dataground.workspace-source-materialization/v1"`.

Use the existing `prepare` and `verify` commands with `/v3` envelope, statement, and trust contracts. For example, set `contract` to `dataground.local-runtime-acceptance-statement/v3` and `profile` to `openshell-codex-workspace-candidate-development/v1` in the statement. The trust profile must name that same profile under its v3 contract and have a new independent digest pin. The signature message starts with `DataGround local candidate runtime acceptance v3`, followed by a newline and the canonical statement bytes. A v2 trust document or signature does not authorize this profile.

The Codex image proof uses the `codex-workspace` verifier. Both its signed raw configuration and independently pulled image must contain the helper contract and the exact DataGround publication source revision. That source revision is bound to `publication.sourceCommit`; the local diagnostic source remains the separately pinned `sourceRevision`. A valid ordinary Codex image signature cannot supply the missing helper binding. Both image publications must have completed successfully when the signing message is prepared. Verification retains the private bounded evidence snapshots, reviewer signature, scope, generation, rejection list, expiry recheck, and strict topology requirements.

The accepted receipt includes `workspaceSourceContract` and the strict image, supervisor, policy, and gateway bindings. It does not authorize a workspace source or select a service plan. The explicit [source worker profile](governed-worker.md#use-an-accepted-source-plan) can consume this acceptance with an independently pinned source plan and grant generation. Source selection, current Cedar authorization, source grants, and lease fencing remain separate requirements. This is a loopback development acceptance boundary; no model-backed v6 acceptance or production release certification is incorporated by this implementation.

## Owned runtime and change-export acceptance

The [version 4 schema](../../deploy/openshell/local-owned-workspace-runtime-acceptance.schema.json)
defines `openshell-codex-workspace-export-candidate-development/v1`. It requires
the complete v7 diagnostic, including all owned sessions and final binary change
export. The runtime image must pass `codex-workspace-export` verification. The
signed raw configuration and pulled image must bind the source, native-owner and
change-export helper contracts to the same publication revision. The separate
strict ARM64 supervisor, policy and gateway requirements remain in effect.

Use the existing `prepare` and `verify` commands with v4 statement, trust and
envelope contracts. The statement and trust use the profile above. In addition
to `workspaceSourceContract`, the statement requires
`nativeOwnerContract: "dataground.native-owner/v1"` and
`workspaceChangesContract: "dataground.workspace-changes/v1"`. Both fields are
returned in the accepted receipt. The signing message starts with
`DataGround local candidate runtime acceptance v4`, followed by a newline and
the canonical statement. Older signatures, trust documents, diagnostics and
source-only image metadata cannot authorize this profile.

The operator still owns the independent trust digest, exact resource scope,
minimum generation and rejection list. Both image publications must have passed
before preparation. Evidence expiry is checked again after external proof
verification. Acceptance does not extend the diagnostic's 24-hour validity.

This standalone profile is not selectable by the worker or publication paths.
It supplies an image and observed-capability binding for the later worker
composition. Current Cedar authority, source grants, durable cleanup and export
records, exclusion of other writers, and retention before deletion remain
separate requirements. No production certification is granted.

## Live acceptance verifier test

`scripts/local-runtime-acceptance.live.test.mjs` is an opt-in test of the real v3 and v4 preparation and verification paths. It reads a fresh model-backed diagnostic for the selected profile, both published image proofs, and a separately acquired Sigstore trusted-root snapshot. It checks the completed GitHub workflow attempts, pulls both exact images, verifies their signatures, signs the prepared bytes with an ephemeral in-memory Ed25519 key, and exercises both the library and the worker-facing verification command. It also requires rejection of changed scope, stale generation, revoked acceptance, wrong pins, expiry, signature tampering, and invalid image signature bundles even when the local test signature is valid.

The test creates a random resource scope and a short-lived test trust profile. It does not write the private key, retain a usable acceptance envelope, configure a worker, acquire provider credentials, or run a model. Temporary public fixture files are removed in `finally`. The test signer does not represent an operational reviewer. A pass proves this verifier composition only; it is not independent operational acceptance, a governed worker invocation, or release acceptance.

Place the eight evidence files listed in the test in a private directory. Use the same bounded owner-only snapshots required by the acceptance command. Create an owner-only JSON input file with exactly these fields, using independently selected publication and diagnostic pins:

```json
{
  "contract": "dataground.test.local-acceptance-input/v1",
  "evidenceDirectory": "/absolute/private/evidence",
  "sourceRevision": "<40-character tested DataGround commit>",
  "model": "<exact diagnostic model>",
  "diagnosticSHA256": "<64-character digest>",
  "trustedRootSHA256": "<independently pinned 64-character digest>",
  "publication": {
    "digest": "sha256:<published Codex manifest digest>",
    "sourceCommit": "<40-character publication commit>",
    "runId": "<completed workflow run>",
    "runAttempt": "1",
    "architecture": "arm64"
  },
  "supervisorPublication": {
    "digest": "sha256:<published supervisor manifest digest>",
    "sourceCommit": "<40-character publication commit>",
    "runId": "<completed workflow run>",
    "runAttempt": "1",
    "architecture": "arm64"
  }
}
```

Put the pinned GitHub CLI 2.98.0 and the repository's Node.js version on `PATH`, with Docker connected to the image store used by the diagnostic. Then run:

```sh
DATAGROUND_TEST_LOCAL_ACCEPTANCE_INPUT=/absolute/private/input.json \
node --test scripts/local-runtime-acceptance.live.test.mjs
```

The diagnostic must have more than five minutes of its 24-hour evidence window left. The test bounds its own acceptance to the earlier of that boundary and 30 minutes from startup. The ordinary repository suite skips this test when the input variable is absent; that skip is not live verification evidence. Invalid or expired supplied inputs fail instead of skipping.

For the v4 verifier test, use `dataground.test.local-acceptance-input/v2` with the
same input fields, a fresh v7 diagnostic, and the export-capable image publication.
This explicit input version selects v4 test trust and its signature domain.
The fixture builder returns `dataground.test.local-acceptance-fixture/v2`; it
cannot be supplied to the existing source-worker fixture consumer.

The v4 live test passed on 2026-10-29 using the retained v7 diagnostic below,
both published images, and a separately acquired authenticated Sigstore root
snapshot with SHA-256
`65ca537f6ed8a47fd0e560c421baa1f6c1efb8b25fc200d8c5c02c0e92eb2b9c`.
Real preparation, offline signature verification, command verification and the
negative cases passed. The test used an ephemeral in-memory key and random
resource scope. It installed no operational acceptance or worker configuration.

The same fixture builder can prepare public test inputs for the separate [live governed source worker test](governed-source-conformance.md):

```sh
node scripts/local-runtime-acceptance-test-fixture.mjs \
  /absolute/private/input.json /absolute/private/new-test-fixture
```

The destination must be new, with an owner-only mode-0700 parent. This command repeats real preparation and verification, generates a random service scope, and writes only the evidence snapshots, public trust, signed envelope, and `fixture.json` with their digest pins. Every file has mode 0600. The private test key stays in memory and is never written. The fixed reviewer and reason identify ephemeral conformance trust; this command cannot accept a caller-selected scope or key. Remove the directory after the test. These files do not represent an operational reviewer, durable trust administration, or release acceptance.

## Owned runtime and workspace-export diagnostic

The local conformance command accepts `--workspace-export` only with
`--workspace-source` and the existing strict candidate selection. This selects
[diagnostic version 7](../../deploy/openshell/runtime-owned-workspace-diagnostic.schema.json).
The selected image must contain the source, native-owner and change-export
helpers. Missing helpers fail the diagnostic. An image label or a successful
source-only diagnostic does not supply this proof.

Every native session in this diagnostic uses the process-owner transport: the
source read-back, eight model cases, artifact production, and a final controlled
writer. Sessions are sequential and have distinct owner bindings. Each must
close with its exact terminal acknowledgement before another starts. Concurrent
session starts are rejected. A failed transport start, missing acknowledgement
or reused binding prevents export evidence. Source materialization stops before
the first native session.

The final probe writes fixed binary content, adds a file and an empty directory,
removes an executable, and starts a detached writer. A second native command
observes that the detached writer continues after its launch command exits.
After confirmed owner cleanup, the probe exports changes through the gateway,
rejects a foreign scope, reconstructs the expected tree, and requires an exact
second export. Its bounded receipt falls inside the artifact case, before the
sandbox teardown case. Only synthetic fixture metadata enters the diagnostic;
private command output and change bytes are not included.

This is a disposable development test. It does not establish exclusion of
writers started outside this launcher, durable retention in the worker, or
permission to delete user workspaces. The existing v1–v3 acceptance contracts
reject this diagnostic. The separate v4 acceptance above can bind it, but a
worker composition is still required before it can authorize governed execution.

The [retained v7 diagnostic](../../deploy/openshell/diagnostics/codex-owned-workspace-candidate-arm64-20261029.json)
passed on 2026-10-29 from clean source
`dbafc6ed3dfdd5c7ebf122d485a79467ad331acd`, using model `gpt-6-astra`.
It used the export-capable ARM64 image
`sha256:89d6c779be2e1ad8afadc22adbec36eb8f0eab3396107237a82829673c66202e`
from [publication run 39078066253, attempt 1](https://github.com/asabla/dataground/actions/runs/39078066253/attempts/1)
and strict supervisor
`sha256:57cc182a7c56cf1fbd64af8b09b2196eea727783eb9ec0c2292542e3be0f755c`.
Both online publication verifiers checked the exact signed invocation, completed
jobs, image and configuration before this run. All eleven native sessions
confirmed cleanup. The final export contained five changes, restored six entries,
and matched its repeated capture. The closed-schema verifier passed with
independently supplied source and image pins. The original record bytes have
SHA-256 `50e7a6d191d121551cee45aec21ccf117f0874e6bbbc0df075b80584b6078774`.
The launcher removed its sandbox, gateway, provider binding, runtime workspace
and consumed credential bundle. Only the empty policy-workspace lock remained
before operator scratch cleanup. This evidence does not change the acceptance
or worker limits above.
