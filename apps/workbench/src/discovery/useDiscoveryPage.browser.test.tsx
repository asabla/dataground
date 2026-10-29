import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import { useDiscoveryPage } from "./useDiscoveryPage";

it("rejects old row actions before a refresh commits and after unmount", async () => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  type Row = { metadata: { id: string } };
  type Result = { ok: true; page: { items: Row[] } };
  let finish!: (value: Result) => void;
  const pending = new Promise<Result>((resolve) => {
    finish = resolve;
  });
  const read = vi.fn(async () =>
    read.mock.calls.length === 1
      ? { ok: true as const, page: { items: [{ metadata: { id: "first" } }] } }
      : pending,
  );
  let control!: ReturnType<typeof useDiscoveryPage<Row>>;
  function View() {
    control = useDiscoveryPage(read, "Discovery stopped.");
    return <p>{control.items.map((item) => item.metadata.id).join(",")}</p>;
  }
  let unmounted = false;
  try {
    await act(async () => root.render(<View />));
    expect(host.textContent).toBe("first");
    const oldAction = control.canAct;
    expect(oldAction()).toBe(true);
    await act(async () => {
      const refresh = control.load();
      finish({ ok: true, page: { items: [{ metadata: { id: "second" } }] } });
      await refresh;
      // React has not committed the queued page state within this act callback.
      expect(oldAction()).toBe(false);
    });
    expect(host.textContent).toBe("second");
    const detached = control;
    expect(detached.canAct()).toBe(true);
    await act(async () => root.unmount());
    unmounted = true;
    expect(detached.canAct()).toBe(false);
    await detached.load();
    expect(read).toHaveBeenCalledTimes(2);
  } finally {
    if (!unmounted) await act(async () => root.unmount());
    host.remove();
  }
});
