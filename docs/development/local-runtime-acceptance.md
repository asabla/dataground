# Local candidate runtime acceptance

The standalone `openshell:local:acceptance` command prepares and verifies an explicitly scoped, signed acceptance of a published candidate and its local runtime diagnostic. Model credentials stay with the local conformance runner. This boundary implements the experimental service-revision allowance in ADR-022: its profile is `openshell-codex-candidate-development/v1`, its deployment scope is `loopback-development-only`, and `certificationEligible` remains false. It does not provide release certification or activate the governed worker. The existing CI evidence and stock runtime-certification contracts remain unchanged.

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

The command is a verification boundary, not a deployment controller. A future governed composition must pin the returned image, model and exact candidate enforcement policy, check acceptance again at consequential effects and renewal, and own generation/revocation state. Reference mode remains the default, and this standalone command cannot make an experimental revision production-ready.
