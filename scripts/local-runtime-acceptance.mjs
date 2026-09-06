import { createHash, createPublicKey, verify } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { isAbsolute, join, normalize, resolve } from "node:path";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import {
  readPrivateSnapshot,
  verifyOfflineAttestationSnapshots,
} from "./check-codex-candidate-attestation.mjs";
import { verifyPublication } from "./check-codex-candidate-publication.mjs";
import { verifyDiagnostic } from "./check-openshell-runtime-diagnostic.mjs";

const root = resolve(import.meta.dirname, "..");
const profile = readFileSync(join(root, "deploy/openshell/development-profile.json"));
const schema = JSON.parse(
  readFileSync(join(root, "deploy/openshell/local-runtime-acceptance.schema.json")),
);
const ajv = new Ajv2020({ strict: true, allErrors: true });
addFormats(ajv);
const validateEnvelope = ajv.compile(schema);
const validateStatement = ajv.compile({ $ref: `${schema.$id}#/$defs/statement` });
const validateTrust = ajv.compile({ $ref: `${schema.$id}#/$defs/trust` });
const day = 24 * 60 * 60 * 1000;
const domain = "DataGround local candidate runtime acceptance v1\n";
const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");

export function canonicalJSON(value) {
  const sort = (item, level) => {
    if (level > 16) throw new Error("Acceptance JSON is too deeply nested.");
    if (Array.isArray(item)) return item.map((child) => sort(child, level + 1));
    if (item !== null && typeof item === "object") {
      return Object.fromEntries(
        Object.keys(item)
          .sort()
          .map((key) => [key, sort(item[key], level + 1)]),
      );
    }
    return item;
  };
  return Buffer.from(`${JSON.stringify(sort(value, 0))}\n`);
}

function parse(bytes, validate) {
  if (!Buffer.isBuffer(bytes) || bytes.length === 0 || bytes.length > 64 << 10) {
    throw new Error("Acceptance document size is invalid.");
  }
  const value = JSON.parse(bytes.toString("utf8"));
  if (!validate(value) || !canonicalJSON(value).equals(bytes)) {
    throw new Error("Acceptance document is not valid canonical JSON.");
  }
  return value;
}

function timestamp(value) {
  const time = Date.parse(value);
  if (!Number.isFinite(time) || new Date(time).toISOString() !== value) {
    throw new Error("Acceptance timestamp is invalid.");
  }
  return time;
}

function sameScope(a, b) {
  return ["isolationDomainId", "serviceId", "revisionId"].every((key) => a?.[key] === b?.[key]);
}

function decode(value, length) {
  const bytes = Buffer.from(value, "base64");
  if (bytes.length !== length || bytes.toString("base64") !== value) {
    throw new Error("Acceptance key or signature encoding is invalid.");
  }
  return bytes;
}

function validateBindings(statement, trustBytes, artifacts, expected) {
  const trust = parse(trustBytes, validateTrust);
  if (
    !/^[a-f0-9]{64}$/.test(expected.trustProfileSHA256 ?? "") ||
    hash(trustBytes) !== expected.trustProfileSHA256 ||
    statement.trustProfileSHA256 !== expected.trustProfileSHA256 ||
    statement.sourceRevision !== expected.sourceRevision ||
    !sameScope(statement.scope, trust.scope) ||
    statement.reviewerId !== trust.reviewerId ||
    statement.profileSHA256 !== hash(profile)
  )
    throw new Error("Acceptance trust, source, scope or profile does not match.");

  const now = expected.now ?? Date.now();
  const issued = timestamp(statement.issuedAt);
  const expires = timestamp(statement.expiresAt);
  if (
    !Number.isSafeInteger(now) ||
    issued > now + 5 * 60 * 1000 ||
    expires <= now ||
    expires <= issued ||
    expires - issued > day ||
    timestamp(trust.notBefore) > issued ||
    timestamp(trust.notBefore) > now ||
    timestamp(trust.notAfter) < expires
  )
    throw new Error("Acceptance or signing trust is premature, expired or too broad.");
  for (const [name, maximum] of Object.entries({
    diagnostic: 64 << 10,
    manifest: 1 << 20,
    imageConfig: 1 << 20,
    bundle: 4 << 20,
    trustedRoot: 1 << 20,
  })) {
    if (
      !Buffer.isBuffer(artifacts[name]) ||
      artifacts[name].length === 0 ||
      artifacts[name].length > maximum
    ) {
      throw new Error("Acceptance evidence snapshot is invalid.");
    }
  }
  if (
    hash(artifacts.diagnostic) !== statement.diagnosticSHA256 ||
    hash(artifacts.imageConfig) !== statement.configSHA256 ||
    hash(artifacts.bundle) !== statement.bundleSHA256 ||
    hash(artifacts.manifest) !== statement.publication.digest.slice(7) ||
    hash(artifacts.trustedRoot) !== trust.trustedRootSHA256
  )
    throw new Error("Acceptance evidence digest does not match.");
  const diagnostic = JSON.parse(artifacts.diagnostic.toString("utf8"));
  if (
    verifyDiagnostic(diagnostic, {
      sourceCommit: statement.sourceRevision,
      candidateImage: statement.localImageId,
    }).length ||
    diagnostic.run.model !== statement.model ||
    issued <= Date.parse(diagnostic.run.finishedAt) ||
    expires > Date.parse(diagnostic.run.finishedAt) + day ||
    ![statement.publication.digest, `sha256:${statement.configSHA256}`].includes(
      statement.localImageId,
    )
  )
    throw new Error("Acceptance diagnostic, model, image identity or evidence age is invalid.");
  decode(trust.publicKey, 32);
  return trust;
}

function verifyImage(statement, trust, artifacts, run) {
  return verifyOfflineAttestationSnapshots(
    statement.publication,
    {
      ...artifacts,
      trustedRootSHA256: trust.trustedRootSHA256,
    },
    run,
  );
}

// The signer vouches for local observations and successful post-signing workflow
// completion. GitHub's image signature alone cannot establish either fact.
export function prepareAcceptance(statementBytes, trustBytes, artifacts, expected, run) {
  const statement = parse(statementBytes, validateStatement);
  const trust = validateBindings(statement, trustBytes, artifacts, expected);
  verifyImage(statement, trust, artifacts, run);
  const publication = verifyPublication(statement.publication, run);
  if (publication.imageId !== statement.localImageId) {
    throw new Error("The pulled published image does not match the local diagnostic.");
  }
  validateBindings(statement, trustBytes, artifacts, expected);
  return Buffer.concat([Buffer.from(domain), statementBytes]);
}

export function verifyAcceptance(envelopeBytes, trustBytes, artifacts, expected, run) {
  const envelope = parse(envelopeBytes, validateEnvelope);
  const statement = envelope.statement;
  if (
    !/^[a-f0-9]{64}$/.test(expected.envelopeSHA256 ?? "") ||
    hash(envelopeBytes) !== expected.envelopeSHA256 ||
    !sameScope(statement.scope, expected.scope) ||
    !Number.isSafeInteger(expected.minimumGeneration) ||
    expected.minimumGeneration < 1 ||
    statement.generation < expected.minimumGeneration ||
    !(expected.rejectedAcceptanceIds instanceof Set) ||
    expected.rejectedAcceptanceIds.has(statement.acceptanceId)
  )
    throw new Error("Acceptance target, digest, generation or revocation does not match.");
  const trust = validateBindings(statement, trustBytes, artifacts, expected);
  const key = createPublicKey({
    key: Buffer.concat([
      Buffer.from("302a300506032b6570032100", "hex"),
      decode(trust.publicKey, 32),
    ]),
    format: "der",
    type: "spki",
  });
  if (
    envelope.signature.keyId !== trust.keyId ||
    !verify(
      null,
      Buffer.concat([Buffer.from(domain), canonicalJSON(statement)]),
      key,
      decode(envelope.signature.value, 64),
    )
  )
    throw new Error("Local acceptance signature is invalid.");
  verifyImage(statement, trust, artifacts, run);
  validateBindings(statement, trustBytes, artifacts, expected);
  return {
    acceptanceId: statement.acceptanceId,
    generation: statement.generation,
    scope: statement.scope,
    profile: statement.profile,
    image: `ghcr.io/asabla/dataground-codex-candidate@${statement.publication.digest}`,
    model: statement.model,
    expiresAt: statement.expiresAt,
    certificationEligible: false,
    deploymentScope: statement.deploymentScope,
  };
}

function acquire(directory) {
  if (!isAbsolute(directory) || normalize(directory) !== directory)
    throw new Error("Evidence directory must be a clean absolute path.");
  return Object.fromEntries(
    Object.entries({
      diagnostic: ["diagnostic.json", 64 << 10],
      manifest: ["manifest.json", 1 << 20],
      imageConfig: ["image-config.json", 1 << 20],
      bundle: ["bundle.jsonl", 4 << 20],
      trustedRoot: ["trusted-root.jsonl", 1 << 20],
    }).map(([key, [file, maximum]]) => [key, readPrivateSnapshot(join(directory, file), maximum)]),
  );
}

if (import.meta.main) {
  try {
    const [
      mode,
      documentFile,
      trustFile,
      directory,
      trustProfileSHA256,
      sourceRevision,
      ...options
    ] = process.argv.slice(2);
    if (
      !["prepare", "verify"].includes(mode) ||
      (mode === "prepare" ? options.length !== 1 : ![5, 6].includes(options.length))
    )
      throw new Error("Invalid arguments.");
    const document = readPrivateSnapshot(documentFile, 64 << 10);
    const trust = readPrivateSnapshot(trustFile, 64 << 10);
    const artifacts = acquire(directory);
    const expected = { trustProfileSHA256, sourceRevision };
    if (mode === "prepare") {
      const [output] = options;
      if (!isAbsolute(output) || normalize(output) !== output)
        throw new Error("Signing output must be a clean absolute path.");
      const message = prepareAcceptance(document, trust, artifacts, expected);
      writeFileSync(output, message, { mode: 0o600, flag: "wx" });
      console.log(
        "Local acceptance signing message prepared; external signature and verification are required.",
      );
    } else {
      const [envelopeSHA256, isolationDomainId, serviceId, revisionId, generation, rejected] =
        options;
      const ids = rejected === undefined ? [] : rejected.split(",");
      if (
        !/^[1-9][0-9]{0,15}$/.test(generation) ||
        ids.some((id) => !/^rtlocal_[0-9a-z]{20,32}$/.test(id))
      )
        throw new Error("Invalid generation or revocation input.");
      console.log(
        JSON.stringify(
          verifyAcceptance(document, trust, artifacts, {
            ...expected,
            envelopeSHA256,
            scope: { isolationDomainId, serviceId, revisionId },
            minimumGeneration: Number(generation),
            rejectedAcceptanceIds: new Set(ids),
          }),
        ),
      );
    }
  } catch {
    console.error("Local runtime acceptance failed; input and upstream details withheld.");
    process.exitCode = 1;
  }
}
