import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createDataGroundClient } from "../contracts/client";
import { ArtifactWorkflow } from "./ArtifactWorkflow";
import type { InvocationArtifact } from "./client";
import { contentArtifact, contentReference, contentResponse } from "./content-test-fixture";

let host: HTMLDivElement;
let root: Root;
const bytes = new TextEncoder().encode("<script>private result</script>\u202e");
let artifact: InvocationArtifact;
beforeEach(async () => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  artifact = await contentArtifact(bytes);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});
function button(label: string) {
  const value = Array.from(host.querySelectorAll("button")).find(
    (entry) => entry.textContent === label,
  );
  expect(value).toBeDefined();
  return value as HTMLButtonElement;
}
async function click(label: string) {
  await act(async () => button(label).click());
}
async function until(predicate: () => boolean) {
  // Flush async browser crypto and React without assuming a timing interval.
  for (let turn = 0; turn < 200 && !predicate(); turn++)
    await act(async () => {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    });
  expect(predicate()).toBe(true);
}
function source() {
  let contentReads = 0;
  let metadataReads = 0;
  let metadata = artifact;
  let signal: AbortSignal | undefined;
  let finish: ((response: Response) => void) | undefined;
  const client = createDataGroundClient("https://api.invalid", {
    bearerToken: "b".repeat(32),
    fetch: async (input, init) => {
      const request = new Request(input, init);
      if (request.url.endsWith("/content")) {
        contentReads++;
        signal = request.signal;
        return new Promise<Response>((resolve) => {
          finish = resolve;
        });
      }
      metadataReads++;
      return new Response(JSON.stringify(metadata), {
        headers: { "Content-Type": "application/json" },
      });
    },
  });
  return {
    client,
    reads: () => contentReads,
    metadataReads: () => metadataReads,
    signal: () => signal,
    metadata: (value: InvocationArtifact) => {
      metadata = value;
    },
    finish: async (response = contentResponse(artifact, bytes)) => {
      await act(async () => finish?.(response));
    },
  };
}
async function mount(value: ReturnType<typeof source>) {
  await act(async () =>
    root.render(<ArtifactWorkflow client={value.client} reference={contentReference} />),
  );
  await until(() => host.textContent?.includes("Artifact content") === true);
}

it("reads only on request, renders verified text safely, and revokes the exact download on hide", async () => {
  const revoked = vi.spyOn(URL, "revokeObjectURL");
  const value = source();
  await mount(value);
  expect(value.reads()).toBe(0);
  await click("Read content");
  expect(value.reads()).toBe(1);
  expect(host.querySelector("pre")).toBeNull();
  expect(host.querySelector("a[download]")).toBeNull();
  await value.finish();
  await until(() => host.querySelector("a[download]") !== null);
  expect(host.querySelector("script")).toBeNull();
  expect(host.querySelector("pre")?.textContent).toBe("<script>private result</script>\\u202e");
  const link = host.querySelector<HTMLAnchorElement>("a[download]");
  expect(link?.download).toBe(`${contentReference.artifactId}.bin`);
  expect(link?.href.startsWith("blob:")).toBe(true);
  const downloaded = new Uint8Array(await (await fetch(link?.href ?? "")).arrayBuffer());
  expect(downloaded).toEqual(bytes);
  const url = link?.href;
  await click("Hide content");
  expect(host.querySelector("pre")).toBeNull();
  expect(host.querySelector("a[download]")).toBeNull();
  expect(revoked).toHaveBeenCalledWith(url);
});

it("cancels late content and requires a new explicit read after refresh", async () => {
  const value = source();
  await mount(value);
  await click("Read content");
  await click("Cancel content read");
  expect(value.signal()?.aborted).toBe(true);
  await value.finish();
  expect(host.querySelector("pre")).toBeNull();
  await click("Read content");
  await value.finish();
  await until(() => host.querySelector("pre") !== null);
  const url = host.querySelector<HTMLAnchorElement>("a[download]")?.href;
  const revoked = vi.spyOn(URL, "revokeObjectURL");
  await click("Refresh metadata");
  expect(value.metadataReads()).toBe(2);
  expect(value.reads()).toBe(2);
  expect(host.querySelector("pre")).toBeNull();
  expect(revoked).toHaveBeenCalledWith(url);
});

it("clears content and aborts old reads when identity, scope, or mounted view changes", async () => {
  const first = source();
  await mount(first);
  await click("Read content");
  await first.finish();
  await until(() => host.querySelector("pre") !== null);
  const revoked = vi.spyOn(URL, "revokeObjectURL");
  const second = source();
  await mount(second);
  expect(host.querySelector("pre")).toBeNull();
  expect(second.reads()).toBe(0);
  expect(revoked).toHaveBeenCalled();
  await click("Read content");
  await act(async () =>
    root.render(
      <ArtifactWorkflow
        client={second.client}
        reference={{ ...contentReference, artifactId: "art_11111111111111111111" }}
      />,
    ),
  );
  expect(second.signal()?.aborted).toBe(true);
  await second.finish();
  expect(host.querySelector("pre")).toBeNull();
  expect(host.querySelector("a[download]")).toBeNull();
  await mount(first);
  await click("Read content");
  await act(async () => root.render(null));
  expect(first.signal()?.aborted).toBe(true);
  await first.finish();
  expect(host.textContent).toBe("");
});

it("withholds corrupt bytes, exposes denial safely, and allows an explicit retry", async () => {
  const value = source();
  await mount(value);
  await click("Read content");
  await value.finish(contentResponse(artifact, new Uint8Array(bytes.length)));
  await until(() => host.querySelector('[role="alert"]') !== null);
  expect(host.querySelector("a[download]")).toBeNull();
  expect(host.querySelector("pre")).toBeNull();
  await click("Retry content read");
  await value.finish(
    new Response(
      JSON.stringify({
        error: { message: "private upstream secret", correlationId: "cor_00000000000000000001" },
      }),
      { status: 403 },
    ),
  );
  await until(() => host.textContent?.includes("current access") === true);
  expect(host.textContent).not.toContain("private upstream secret");
  expect(host.textContent).toContain("cor_00000000000000000001");
  await click("Retry content read");
  await value.finish();
  await until(() => host.querySelector("pre") !== null);
});

it("withholds read controls for deleted or oversized artifacts", async () => {
  for (const metadata of [
    { ...artifact, state: "deleted" },
    { ...artifact, sizeBytes: 16 * 1024 * 1024 + 1 },
  ]) {
    const value = source();
    value.metadata(metadata);
    await mount(value);
    expect(host.textContent).not.toContain("Read content");
    expect(value.reads()).toBe(0);
  }
});
