import { Button, TextField } from "@dataground/ui";
import { useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import type { AgentServiceFailure } from "../services/client";
import { createWorkspace, type WorkspaceResource } from "./client";

interface Attempt {
  key: string;
  request: { name: string; description?: string };
}
export function WorkspaceCreate({
  client,
  isolationDomainId,
  onCreated,
}: {
  client: DataGroundClient;
  isolationDomainId: string;
  onCreated: (workspace: WorkspaceResource) => void;
}) {
  const [name, setName] = useState(""),
    [description, setDescription] = useState(""),
    [attempt, setAttempt] = useState<Attempt>(),
    [pending, setPending] = useState(false),
    [error, setError] = useState<AgentServiceFailure>(),
    [created, setCreated] = useState<WorkspaceResource>();
  const generation = useRef(0),
    busy = useRef(false),
    identity = useRef({ client, isolationDomainId });
  identity.current = { client, isolationDomainId };
  useEffect(
    () => () => {
      generation.current++;
    },
    [],
  );
  const submit = async () => {
    if (busy.current || created) return;
    let next = attempt;
    if (!next) {
      if (!name.trim()) {
        setError({
          code: "WORKSPACE_NAME_REQUIRED",
          message: "Workspace name is required.",
          retryable: false,
        });
        return;
      }
      try {
        next = {
          key: `workspace:${crypto.randomUUID().replaceAll("-", "")}`,
          request: { name: name.trim(), ...(description ? { description } : {}) },
        };
      } catch {
        setError({
          code: "WORKBENCH_SECURE_RANDOM_UNAVAILABLE",
          message: "A secure request identifier could not be created. Refresh before retrying.",
          retryable: false,
        });
        return;
      }
    }
    busy.current = true;
    const request = ++generation.current;
    setPending(true);
    setAttempt(next);
    setError(undefined);
    const result = await createWorkspace(client, isolationDomainId, next.request, next.key);
    if (
      request !== generation.current ||
      identity.current.client !== client ||
      identity.current.isolationDomainId !== isolationDomainId
    )
      return;
    busy.current = false;
    setPending(false);
    if (result.ok) {
      setCreated(result.workspace);
      setAttempt(undefined);
      setName("");
      setDescription("");
      onCreated(result.workspace);
    } else {
      setError(result.error);
      if (!result.error.retryable && !result.error.outcomeUnknown) setAttempt(undefined);
    }
  };
  return (
    <section aria-label="Create workspace" className="workspace-create">
      <h2>New workspace</h2>
      <p>Organize related agent services. Access to each service remains separate.</p>
      {created ? (
        <p role="status">
          Created {created.name}.{" "}
          <Button variant="quiet" onPress={() => onCreated(created)}>
            Open workspace
          </Button>
        </p>
      ) : (
        <>
          <TextField
            label="Workspace name"
            value={name}
            onChange={setName}
            isDisabled={pending || !!attempt}
          />
          <TextField
            label="Workspace description"
            value={description}
            onChange={setDescription}
            isMultiline
            isDisabled={pending || !!attempt}
          />
          {error && <p role="alert">{error.message}</p>}
          {pending && <p role="status">Creating workspace…</p>}
          {attempt && !pending && (
            <p>The saved request will be sent again with the same identifier and fields.</p>
          )}
          <Button variant="primary" isDisabled={pending} onPress={() => void submit()}>
            {attempt ? "Retry workspace creation" : "Create workspace"}
          </Button>
        </>
      )}
    </section>
  );
}
