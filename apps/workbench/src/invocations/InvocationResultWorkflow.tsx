import { InvocationResult } from "@dataground/patterns";
import { useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  type InvocationResultRead,
  type InvocationResultReference,
  readInvocationResult,
} from "./client";

export function InvocationResultWorkflow({
  client,
  reference,
}: {
  client: DataGroundClient;
  reference: InvocationResultReference;
}) {
  const key = `${reference.isolationDomainId}:${reference.invocationId}:${reference.serviceId}:${reference.revisionId}`;
  const generation = useRef(0);
  const pending = useRef(false);
  const [state, setState] = useState<{
    client: DataGroundClient;
    key: string;
    loading: boolean;
    result?: InvocationResultRead;
  }>({ client, key, loading: false });
  useEffect(() => {
    generation.current++;
    pending.current = false;
    setState({ client, key, loading: false });
    return () => {
      generation.current++;
      pending.current = false;
    };
  }, [client, key]);
  const visible = state.client === client && state.key === key ? state : undefined;
  async function show() {
    if (pending.current) return;
    pending.current = true;
    const request = ++generation.current;
    setState({ client, key, loading: true });
    const result = await readInvocationResult(client, reference);
    if (generation.current === request) {
      pending.current = false;
      setState({ client, key, loading: false, result });
    }
  }
  function hide() {
    generation.current++;
    pending.current = false;
    setState({ client, key, loading: false });
  }
  return (
    <InvocationResult
      error={visible?.result && !visible.result.ok ? visible.result.error : undefined}
      isLoading={visible?.loading}
      onHide={hide}
      onShow={() => void show()}
      text={visible?.result?.ok ? visible.result.text : undefined}
    />
  );
}
