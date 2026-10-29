import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import type { ServiceRevisionHistoryResource } from "./client";
import { ServiceRevisionHistoryPanel } from "./ServiceRevisionHistoryPanel";

it("filters loaded versions and disables retained table actions after a failed refresh", async () => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const onOpen = vi.fn();
  const revisions: ServiceRevisionHistoryResource[] = ["draft", "published"].map(
    (state, index) => ({
      state: state as "draft" | "published",
      revisionNumber: index + 1,
      runtimeProfile: "reference/v1",
      requiredCapabilities: [],
      serviceId: "svc_00000000000000000001",
      metadata: {
        id: `rev_${String(index + 1).padStart(20, "0")}`,
        isolationDomainId: "iso_00000000000000000001",
        version: 1,
        generation: 1,
        createdAt: "2026-10-29T00:00:00Z",
        updatedAt: "2026-10-29T00:00:00Z",
        createdBy: "review",
      },
      ...(state === "published" ? { publishedAt: "2026-10-29T00:00:00Z" } : {}),
    }),
  );
  const render = async (denied = false) =>
    act(async () =>
      root.render(
        <ServiceRevisionHistoryPanel
          revisions={revisions}
          isLoading={false}
          isLoadingMore={false}
          onLoadMore={() => {}}
          onRetry={() => {}}
          onOpen={onOpen}
          error={
            denied ? { code: "DENIED", message: "Access denied", retryable: false } : undefined
          }
        />,
      ),
    );
  try {
    await render();
    const first = host.querySelector("tbody tr");
    expect(first?.textContent).toContain("Revision 2");
    const filter = host.querySelector("select") as HTMLSelectElement;
    await act(async () => {
      filter.value = "draft";
      filter.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(host.querySelectorAll("tbody tr")).toHaveLength(1);
    expect(host.querySelector("tbody tr")?.textContent).toContain("Revision 1");
    const opener = host.querySelector('[aria-label="Open revision 1"]') as HTMLButtonElement;
    await render(true);
    expect(opener.disabled).toBe(true);
    await act(async () => opener.click());
    expect(onOpen).not.toHaveBeenCalled();
  } finally {
    await act(async () => root.unmount());
    host.remove();
  }
});
