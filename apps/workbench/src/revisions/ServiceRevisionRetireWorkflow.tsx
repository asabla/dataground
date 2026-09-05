import { Button, StatusBadge } from "@dataground/ui";
import { useEffect, useId, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  retireServiceRevision,
  type ServiceRevisionFailure,
  type ServiceRevisionHistoryResource,
} from "./client";

export interface RetirementAttempt {
  idempotencyKey: string;
  revision: ServiceRevisionHistoryResource;
}

export function prepareRetirementAttempt(
  revision: ServiceRevisionHistoryResource,
  randomUUID: () => string,
): RetirementAttempt {
  return {
    idempotencyKey: `retirement:${randomUUID().replaceAll("-", "")}`,
    revision: JSON.parse(JSON.stringify(revision)) as ServiceRevisionHistoryResource,
  };
}

export interface ServiceRevisionRetireWorkflowProps {
  client: DataGroundClient;
  revision: ServiceRevisionHistoryResource;
  canRetire: boolean;
  onClose: () => void;
  onRetired: (revision: ServiceRevisionHistoryResource) => void;
}

export function ServiceRevisionRetireWorkflow({
  client,
  revision,
  canRetire,
  onClose,
  onRetired,
}: ServiceRevisionRetireWorkflowProps) {
  const titleId = useId();
  const title = useRef<HTMLHeadingElement>(null);
  const feedback = useRef<HTMLDivElement>(null);
  const generation = useRef(0);
  const lock = useRef(false);
  const scope = `${revision.metadata.isolationDomainId}/${revision.metadata.id}/${revision.metadata.version}`;
  const [state, setState] = useState<{
    client: DataGroundClient;
    scope: string;
    pending: boolean;
    attempt?: RetirementAttempt;
    error?: ServiceRevisionFailure;
    retired?: ServiceRevisionHistoryResource;
  }>({ client, scope, pending: false });
  useEffect(() => {
    generation.current++;
    lock.current = false;
    setState({ client, scope, pending: false });
    title.current?.focus();
    return () => {
      generation.current++;
    };
  }, [client, scope]);
  const current = state.client === client && state.scope === scope;
  const pending = current && state.pending;
  const retired = current && state.retired;
  const problem = current && state.error;
  useEffect(() => {
    if (problem || retired) feedback.current?.focus();
  }, [problem, retired]);
  const permitted = canRetire && revision.state === "published";
  const confirm = async () => {
    if (!current || !permitted || lock.current || pending || retired) return;
    let attempt = state.attempt;
    if (!attempt) {
      try {
        attempt = prepareRetirementAttempt(revision, () => globalThis.crypto.randomUUID());
      } catch {
        setState({
          client,
          scope,
          pending: false,
          error: {
            code: "WORKBENCH_RETIREMENT_REQUEST_UNAVAILABLE",
            message:
              "A stable retirement request could not be prepared. Reopen the revision before retrying.",
            retryable: false,
          },
        });
        return;
      }
    }
    lock.current = true;
    const requestGeneration = generation.current;
    setState({ client, scope, pending: true, attempt });
    try {
      const result = await retireServiceRevision(client, attempt.revision, attempt.idempotencyKey);
      if (generation.current !== requestGeneration) return;
      if (result.ok) {
        setState({ client, scope, pending: false, attempt, retired: result.revision });
        onRetired(result.revision);
      } else {
        setState({ client, scope, pending: false, attempt, error: result.error });
      }
    } finally {
      if (generation.current === requestGeneration) lock.current = false;
    }
  };
  return (
    <section className="product-workflow__inspection" aria-labelledby={titleId}>
      <h2 id={titleId} tabIndex={-1} ref={title}>
        Retire revision {revision.revisionNumber}
      </h2>
      <p>
        <code>{revision.metadata.id}</code> · Version {revision.metadata.version}
      </p>
      <p>
        Retirement permanently stops new routing and repair through this revision. Existing
        definitions, invocation history, artifacts, and audit remain available under their access
        policies.
      </p>
      {!retired && (
        <p>
          Move every alias away and finish or cancel active work first. DataGround checks these
          conditions when the command commits.
        </p>
      )}
      {retired ? (
        <div role="status" ref={feedback} tabIndex={-1}>
          <StatusBadge tone="success">Revision retired</StatusBadge>
          <p>DataGround confirmed retirement of revision {retired.revisionNumber}.</p>
        </div>
      ) : !permitted ? (
        <p role="alert">Retirement is unavailable for this revision or permission.</p>
      ) : null}
      {problem && (
        <div role="alert" className="workbench-inline-error" ref={feedback} tabIndex={-1}>
          <p>{problem.message}</p>
          {problem.correlationId && (
            <p>
              Correlation: <code>{problem.correlationId}</code>
            </p>
          )}
        </div>
      )}
      {pending && <p role="status">Confirming revision retirement…</p>}
      {!retired && permitted && (!problem || problem.retryable) && (
        <Button
          isDisabled={pending || !current}
          onPress={() => {
            void confirm();
          }}
        >
          {problem ? "Recover retirement request" : "Confirm retirement"}
        </Button>
      )}
      <Button isDisabled={pending} variant="quiet" onPress={onClose}>
        {retired ? "Back to revisions" : "Close retirement"}
      </Button>
    </section>
  );
}
