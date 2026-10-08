import type { DataGroundClient } from "../contracts/client";
import type { ArtifactFailure, InvocationArtifact, InvocationArtifactReference } from "./client";

export const maximumArtifactContentBytes = 16 * 1024 * 1024;
const maximumPreviewBytes = 1024 * 1024;
const maximumErrorBytes = 8192;

export type ArtifactContentResult =
  | { ok: true; bytes: Uint8Array<ArrayBuffer>; text?: string; previewMessage?: string }
  | { ok: false; error: ArtifactFailure };

function failure(code: string, message: string, retryable = false): ArtifactContentResult {
  return { ok: false, error: { code, message, retryable } };
}

export function canReadArtifactContent(artifact: InvocationArtifact): boolean {
  return (
    artifact.state === "available" &&
    Number.isSafeInteger(artifact.sizeBytes) &&
    artifact.sizeBytes >= 0 &&
    artifact.sizeBytes <= maximumArtifactContentBytes
  );
}

async function readBounded(
  response: Response,
  maximum: number,
  signal: AbortSignal,
): Promise<Uint8Array<ArrayBuffer>> {
  const length = response.headers.get("Content-Length");
  if (length !== null && (!/^(0|[1-9][0-9]*)$/u.test(length) || Number(length) > maximum)) {
    await response.body?.cancel();
    throw new Error("body limit");
  }
  if (!response.body) return new Uint8Array();
  const reader = response.body.getReader();
  const bytes = new Uint8Array(maximum);
  let size = 0;
  let complete = false;
  const abort = () => {
    void reader.cancel().catch(() => {});
  };
  signal.addEventListener("abort", abort, { once: true });
  try {
    for (;;) {
      signal.throwIfAborted();
      const next = await reader.read();
      signal.throwIfAborted();
      if (next.done) break;
      if (size + next.value.byteLength > maximum) throw new Error("body limit");
      bytes.set(next.value, size);
      size += next.value.byteLength;
    }
    complete = true;
    return bytes.slice(0, size);
  } finally {
    signal.removeEventListener("abort", abort);
    if (!complete) await reader.cancel().catch(() => {});
    reader.releaseLock();
    bytes.fill(0);
  }
}

function contentFailure(status: number, bytes: Uint8Array): ArtifactContentResult {
  let code = "WORKBENCH_ARTIFACT_CONTENT_UNAVAILABLE";
  let message = "Artifact content could not be read.";
  let retryable = status === 503 || status === 429;
  if (status === 401 || status === 403) {
    code = "WORKBENCH_ARTIFACT_CONTENT_FORBIDDEN";
    message = "Your current access does not allow this content read.";
  } else if (status === 404) {
    message = "Artifact content was not found in this invocation.";
  }
  let correlationId: string | undefined;
  try {
    const value: unknown = JSON.parse(new TextDecoder().decode(bytes));
    if (
      typeof value === "object" &&
      value !== null &&
      "error" in value &&
      typeof value.error === "object" &&
      value.error !== null &&
      "correlationId" in value.error &&
      typeof value.error.correlationId === "string" &&
      /^cor_[0-9a-z]{20,32}$/u.test(value.error.correlationId)
    )
      correlationId = value.error.correlationId;
  } catch {
    retryable = false;
  }
  return { ok: false, error: { code, message, retryable, status, correlationId } };
}

function preview(bytes: Uint8Array, mediaType: string): { text?: string; previewMessage?: string } {
  if (bytes.byteLength > maximumPreviewBytes)
    return { previewMessage: "Content is verified. Download it to read files larger than 1 MiB." };
  if (!/^(text\/plain|application\/json)(?:;\s*charset=utf-8)?$/iu.test(mediaType))
    return { previewMessage: "Content is verified. This file type is available as a download." };
  try {
    const text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    return {
      text: text.replace(
        // biome-ignore lint/suspicious/noControlCharactersInRegex: Render control characters visibly instead of interpreting them.
        /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/gu,
        (character) => `\\u${character.charCodeAt(0).toString(16).padStart(4, "0")}`,
      ),
    };
  } catch {
    return {
      previewMessage:
        "Content is verified but is not valid UTF-8 text. Download the original bytes.",
    };
  }
}

// The metadata and response must describe the same exact bytes. No partial
// content or upstream error text enters presentation state.
export async function readArtifactContent(
  client: DataGroundClient,
  reference: InvocationArtifactReference,
  artifact: InvocationArtifact,
  signal: AbortSignal,
): Promise<ArtifactContentResult> {
  if (
    !/^iso_[0-9a-z]{20,32}$/u.test(reference.isolationDomainId) ||
    !/^inv_[0-9a-z]{20,32}$/u.test(reference.invocationId) ||
    !/^art_[0-9a-z]{20,32}$/u.test(reference.artifactId) ||
    artifact.metadata.isolationDomainId !== reference.isolationDomainId ||
    artifact.invocationId !== reference.invocationId ||
    artifact.metadata.id !== reference.artifactId ||
    !/^sha256:[0-9a-f]{64}$/u.test(artifact.digest) ||
    !canReadArtifactContent(artifact)
  )
    return failure(
      "WORKBENCH_INVALID_REFERENCE",
      "Available artifact content up to 16 MiB is required.",
    );
  // Bound stalled fetches as well as body reads, including test/custom transports
  // which return a stream independently of the fetch signal.
  let boundedSignal = signal;
  let bytes: Uint8Array<ArrayBuffer> | undefined;
  try {
    boundedSignal = AbortSignal.any([signal, AbortSignal.timeout(30_000)]);
    boundedSignal.throwIfAborted();
    const response = await client.fetchArtifactContent(reference, boundedSignal);
    if (boundedSignal.aborted) {
      await response.body?.cancel();
      boundedSignal.throwIfAborted();
    }
    if (!response.ok) {
      bytes = await readBounded(response, maximumErrorBytes, boundedSignal);
      return contentFailure(response.status, bytes);
    }
    if (
      response.status !== 200 ||
      response.redirected ||
      response.headers.get("Content-Type") !== "application/octet-stream" ||
      response.headers.get("ETag") !== `"${artifact.digest}"` ||
      response.headers.get("Content-Length") !== String(artifact.sizeBytes)
    ) {
      await response.body?.cancel();
      return failure(
        "WORKBENCH_ARTIFACT_CONTENT_MISMATCH",
        "Artifact content did not match its metadata.",
      );
    }
    bytes = await readBounded(response, artifact.sizeBytes, boundedSignal);
    if (bytes.byteLength !== artifact.sizeBytes)
      return failure(
        "WORKBENCH_ARTIFACT_CONTENT_MISMATCH",
        "Artifact content did not match its metadata.",
      );
    const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
    boundedSignal.throwIfAborted();
    const digest = `sha256:${Array.from(hash, (value) => value.toString(16).padStart(2, "0")).join("")}`;
    if (digest !== artifact.digest)
      return failure(
        "WORKBENCH_ARTIFACT_CONTENT_MISMATCH",
        "Artifact content did not match its metadata.",
      );
    const result: ArtifactContentResult = {
      ok: true,
      bytes,
      ...preview(bytes, artifact.mediaType),
    };
    bytes = undefined;
    return result;
  } catch {
    return failure(
      boundedSignal.aborted
        ? "WORKBENCH_ARTIFACT_CONTENT_CANCELLED"
        : "WORKBENCH_ARTIFACT_CONTENT_UNAVAILABLE",
      boundedSignal.aborted
        ? "The content read was cancelled or timed out."
        : "Artifact content could not be read or verified.",
      true,
    );
  } finally {
    bytes?.fill(0);
  }
}
