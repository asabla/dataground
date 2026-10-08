import type { InvocationArtifact } from "./client";

export const contentReference = {
  artifactId: "art_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};
export async function contentArtifact(
  bytes: Uint8Array<ArrayBuffer>,
  mediaType = "text/plain; charset=utf-8",
): Promise<InvocationArtifact> {
  const digest = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), (value) =>
    value.toString(16).padStart(2, "0"),
  ).join("");
  return {
    digest: `sha256:${digest}`,
    invocationId: contentReference.invocationId,
    kind: "file",
    mediaType,
    name: "untrusted-name.html",
    sensitive: true,
    sizeBytes: bytes.byteLength,
    state: "available",
    metadata: {
      id: contentReference.artifactId,
      isolationDomainId: contentReference.isolationDomainId,
      createdAt: "2026-09-05T12:00:00Z",
      updatedAt: "2026-09-05T12:00:00Z",
      createdBy: "worker",
      version: 1,
      generation: 1,
    },
  };
}
export function contentResponse(artifact: InvocationArtifact, body: BodyInit | null): Response {
  return new Response(body, {
    status: 200,
    headers: {
      "Content-Type": "application/octet-stream",
      "Content-Length": String(artifact.sizeBytes),
      ETag: `"${artifact.digest}"`,
    },
  });
}
