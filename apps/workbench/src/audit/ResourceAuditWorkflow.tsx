import { ResourceAudit } from "@dataground/patterns";
import { useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import { type ResourceAuditRead, type ResourceAuditReference, readResourceAudit } from "./client";

export function ResourceAuditWorkflow({
  client,
  reference,
}: {
  client: DataGroundClient;
  reference: ResourceAuditReference;
}) {
  const scope = `${reference.isolationDomainId}/${reference.resourceType}/${reference.resourceId}`;
  const generation = useRef(0);
  const pending = useRef(false);
  const identity = useRef({ client, scope });
  identity.current = { client, scope };
  const [state, setState] = useState<{
    client: DataGroundClient;
    scope: string;
    loading: boolean;
    result?: ResourceAuditRead;
  }>({ client, scope, loading: false });
  useEffect(() => {
    generation.current++;
    pending.current = false;
    setState({ client, scope, loading: false });
    return () => {
      generation.current++;
      pending.current = false;
    };
  }, [client, scope]);
  const current = state.client === client && state.scope === scope ? state : undefined;
  async function read(cursor?: string) {
    if (pending.current || identity.current.client !== client || identity.current.scope !== scope)
      return;
    pending.current = true;
    const request = ++generation.current;
    const previous = current?.result?.ok ? current.result.page : undefined;
    setState({ client, scope, loading: true });
    const result = await readResourceAudit(client, reference, cursor);
    if (
      generation.current !== request ||
      identity.current.client !== client ||
      identity.current.scope !== scope
    )
      return;
    pending.current = false;
    // Reject repetition of the preceding page even with a new receipt. Retain
    // only the current page; do not accumulate audit payloads.
    if (
      cursor &&
      result.ok &&
      previous &&
      (result.page.items.some((item) => previous.items.some((old) => old.id === item.id)) ||
        (previous.items.some((item) => item.source !== "lifecycle") &&
          result.page.items.some((item) => item.source === "lifecycle")))
    ) {
      setState({
        client,
        scope,
        loading: false,
        result: {
          ok: false,
          error: {
            code: "WORKBENCH_AUDIT_STALLED",
            message: "The audit page did not advance. Refresh the audit to start a new read.",
          },
        },
      });
    } else setState({ client, scope, loading: false, result });
  }
  function hide() {
    generation.current++;
    pending.current = false;
    setState({ client, scope, loading: false });
  }
  const page = current?.result?.ok ? current.result.page : undefined;
  return (
    <ResourceAudit
      title={
        reference.resourceType === "invocation" ? "Invocation audit" : "Service revision audit"
      }
      resourceId={reference.resourceId}
      items={page?.items}
      receiptId={page?.receiptId}
      hasNextPage={page?.nextCursor !== undefined}
      isLoading={current?.loading}
      error={current?.result && !current.result.ok ? current.result.error : undefined}
      onRead={() => void read()}
      onNext={() => {
        if (page?.nextCursor) void read(page.nextCursor);
      }}
      onHide={hide}
    />
  );
}
