import assert from "node:assert/strict";
import { describe, it, vi } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { readArtifactContent } from "./content";
import { contentArtifact, contentReference, contentResponse } from "./content-test-fixture";

function client(response: Response): DataGroundClient {
  return { fetchArtifactContent: async () => response } as unknown as DataGroundClient;
}
const data = new TextEncoder().encode("<script>private</script>\u202e\u0000");

describe("artifact content verification", () => {
  it("preserves exact verified bytes and makes directional controls visible", async () => {
    const artifact = await contentArtifact(data);
    const result = await readArtifactContent(
      client(contentResponse(artifact, data)),
      contentReference,
      artifact,
      new AbortController().signal,
    );
    assert.ok(result.ok);
    assert.deepEqual(result.bytes, data);
    assert.equal(result.text, "<script>private</script>\\u202e\\u0000");
  });
  it("accepts an empty file and leaves binary or larger previews to verified download", async () => {
    for (const bytes of [
      new Uint8Array(),
      new Uint8Array([255, 0, 1]),
      new Uint8Array(1024 * 1024 + 1),
    ]) {
      const artifact = await contentArtifact(bytes);
      const result = await readArtifactContent(
        client(contentResponse(artifact, bytes)),
        contentReference,
        artifact,
        new AbortController().signal,
      );
      assert.ok(result.ok);
      assert.deepEqual(result.bytes, bytes);
      if (bytes.length === 0) assert.equal(result.text, "");
      else {
        assert.equal(result.text, undefined);
        assert.ok(result.previewMessage);
      }
    }
    const artifact = await contentArtifact(data, "text/html");
    const result = await readArtifactContent(
      client(contentResponse(artifact, data)),
      contentReference,
      artifact,
      new AbortController().signal,
    );
    assert.ok(result.ok);
    assert.equal(result.text, undefined);
  });
  it("rejects foreign, unavailable, and oversized descriptors before transport", async () => {
    const artifact = await contentArtifact(data);
    let calls = 0;
    const source = {
      fetchArtifactContent: async () => {
        calls++;
        return contentResponse(artifact, data);
      },
    } as unknown as DataGroundClient;
    for (const candidate of [
      { ...artifact, invocationId: "inv_11111111111111111111" },
      {
        ...artifact,
        metadata: { ...artifact.metadata, isolationDomainId: "iso_11111111111111111111" },
      },
      { ...artifact, metadata: { ...artifact.metadata, id: "art_11111111111111111111" } },
      { ...artifact, digest: "sha256:bad" },
      { ...artifact, sizeBytes: 16 * 1024 * 1024 + 1 },
      { ...artifact, sizeBytes: -1 },
      { ...artifact, state: "deleted" },
      { ...artifact, state: "quarantined" },
    ]) {
      const result = await readArtifactContent(
        source,
        contentReference,
        candidate,
        new AbortController().signal,
      );
      assert.equal(result.ok, false);
    }
    assert.equal(calls, 0);
  });
  it("withholds corrupt, truncated, extra, redirected, or mismatched content", async () => {
    const artifact = await contentArtifact(data);
    for (const name of ["digest", "short", "extra", "etag", "size", "type", "status", "redirect"]) {
      const body =
        name === "digest"
          ? new Uint8Array(data.length)
          : name === "short"
            ? data.slice(0, -1)
            : name === "extra"
              ? new Uint8Array(data.length + 1)
              : data;
      const response = contentResponse(artifact, body);
      if (name === "etag") response.headers.set("ETag", '"foreign"');
      if (name === "size") response.headers.set("Content-Length", "1");
      if (name === "type") response.headers.set("Content-Type", "text/html");
      if (name === "status") Object.defineProperty(response, "status", { value: 206 });
      if (name === "redirect") Object.defineProperty(response, "redirected", { value: true });
      const result = await readArtifactContent(
        client(response),
        contentReference,
        artifact,
        new AbortController().signal,
      );
      assert.equal(result.ok, false, name);
      assert.equal("bytes" in result, false, name);
    }
  });
  it("bounds error bodies and never displays upstream messages", async () => {
    const artifact = await contentArtifact(data);
    for (const body of [
      JSON.stringify({
        error: { message: "private upstream secret", correlationId: "cor_00000000000000000001" },
      }),
      "x".repeat(8193),
    ]) {
      const result = await readArtifactContent(
        client(new Response(body, { status: 403 })),
        contentReference,
        artifact,
        new AbortController().signal,
      );
      assert.equal(result.ok, false);
      assert.doesNotMatch(JSON.stringify(result), /private upstream secret|x{20}/u);
      if (!result.ok && body.startsWith("{"))
        assert.equal(result.error.correlationId, "cor_00000000000000000001");
    }
  });
  it("cancels a stalled body and suppresses late bytes", async () => {
    const artifact = await contentArtifact(data);
    const abort = new AbortController();
    let reading: (() => void) | undefined;
    const started = new Promise<void>((resolve) => {
      reading = resolve;
    });
    let cancelled = false;
    const body = new ReadableStream<Uint8Array>({
      pull() {
        reading?.();
      },
      cancel() {
        cancelled = true;
      },
    });
    const task = readArtifactContent(
      client(contentResponse(artifact, body)),
      contentReference,
      artifact,
      abort.signal,
    );
    await started;
    abort.abort();
    const result = await task;
    assert.equal(result.ok, false);
    assert.equal(cancelled, true);
  });
});

it("fails closed when the browser cannot construct the bounded abort signal", async () => {
  const artifact = await contentArtifact(data);
  const unsupported = vi.spyOn(AbortSignal, "any").mockImplementation(() => {
    throw new TypeError("unsupported");
  });
  try {
    const result = await readArtifactContent(
      client(contentResponse(artifact, data)),
      contentReference,
      artifact,
      new AbortController().signal,
    );
    assert.equal(result.ok, false);
    assert.equal("bytes" in result, false);
  } finally {
    unsupported.mockRestore();
  }
});
