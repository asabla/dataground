import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import type { ServiceRevisionResource } from "./client";
import { RevisionContractReview } from "./RevisionContractReview";

let host: HTMLDivElement;
let root: Root;
const client = {} as DataGroundClient;
const revision: ServiceRevisionResource = {
  metadata: {
    id: "rev_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    createdAt: "2026-01-01T00:00:00Z",
    updatedAt: "2026-01-01T00:00:00Z",
    createdBy: "usr_00000000000000000001",
    version: 1,
    generation: 1,
  },
  serviceId: "svc_00000000000000000001",
  revisionNumber: 1,
  state: "draft",
  runtimeProfile: "reference/v1",
  requiredCapabilities: [],
  inputSchema: { description: "<script>untrusted</script>", type: "object" },
};

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});
async function render(current = revision, connection = client) {
  await act(async () =>
    root.render(<RevisionContractReview client={connection} revision={current} />),
  );
}
async function toggle() {
  const button = host.querySelector("button");
  expect(button).not.toBeNull();
  button?.focus();
  await act(async () => button?.click());
}

it("reveals exact escaped JSON on demand and removes it on hide while retaining focus", async () => {
  await render();
  expect(host.querySelector("pre")).toBeNull();
  await toggle();
  expect(host.querySelector("pre")?.textContent).toContain("<script>untrusted</script>");
  expect(host.querySelector("script")).toBeNull();
  expect(host.textContent).toContain("cannot render this input contract");
  expect(host.textContent).toContain("No output contract is declared");
  expect(
    host.querySelector<HTMLElement>("section[aria-label='Input contract JSON']")?.tabIndex,
  ).toBe(0);
  await toggle();
  expect(host.querySelector("pre")).toBeNull();
  expect(document.activeElement).toBe(host.querySelector("button"));
});

it.each(["scope", "version", "connection", "loaded object"])(
  "hides contracts when the %s changes",
  async (change) => {
    await render();
    await toggle();
    const next =
      change === "scope"
        ? {
            ...revision,
            metadata: { ...revision.metadata, isolationDomainId: "iso_00000000000000000002" },
          }
        : change === "version"
          ? { ...revision, metadata: { ...revision.metadata, version: 2 } }
          : change === "loaded object"
            ? { ...revision }
            : revision;
    await render(next, change === "connection" ? ({} as DataGroundClient) : client);
    expect(host.querySelector("pre")).toBeNull();
    expect(host.textContent).not.toContain("<script>untrusted</script>");
    expect(host.querySelector("button")?.getAttribute("aria-expanded")).toBe("false");
  },
);
