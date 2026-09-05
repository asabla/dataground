import { Button, StatusBadge } from "@dataground/ui";
import { useEffect, useId, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  type ServiceAliasFailure,
  type ServiceAliasResource,
  type WithdrawnServiceAliasResource,
  withdrawServiceAlias,
} from "./aliasClient";

export interface AliasWithdrawalAttempt {
  alias: ServiceAliasResource;
  idempotencyKey: string;
}
export function prepareAliasWithdrawal(
  alias: ServiceAliasResource,
  randomUUID: () => string,
): AliasWithdrawalAttempt {
  return {
    alias: { ...alias, metadata: { ...alias.metadata } },
    idempotencyKey: `alias-withdrawal:${randomUUID().replaceAll("-", "")}`,
  };
}
export interface ServiceAliasWithdrawWorkflowProps {
  alias: ServiceAliasResource;
  canWithdraw: boolean;
  client: DataGroundClient;
  onClose: () => void;
  onWithdrawn: (alias: WithdrawnServiceAliasResource) => void;
}
export function ServiceAliasWithdrawWorkflow({
  alias,
  canWithdraw,
  client,
  onClose,
  onWithdrawn,
}: ServiceAliasWithdrawWorkflowProps) {
  const titleId = useId();
  const title = useRef<HTMLHeadingElement>(null);
  const feedback = useRef<HTMLDivElement>(null);
  const generation = useRef(0);
  const lock = useRef(false);
  const scope = `${alias.metadata.isolationDomainId}/${alias.serviceId}/${alias.metadata.id}/${alias.name}/${alias.revisionId}/${alias.metadata.version}`;
  const [state, setState] = useState<{
    client: DataGroundClient;
    scope: string;
    pending: boolean;
    attempt?: AliasWithdrawalAttempt;
    error?: ServiceAliasFailure;
    withdrawn?: WithdrawnServiceAliasResource;
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
  const problem = current ? state.error : undefined;
  const withdrawn = current ? state.withdrawn : undefined;
  useEffect(() => {
    if (problem || withdrawn) feedback.current?.focus();
  }, [problem, withdrawn]);
  async function confirm() {
    if (
      !current ||
      !canWithdraw ||
      pending ||
      lock.current ||
      withdrawn ||
      (problem && !problem.retryable)
    )
      return;
    let attempt = state.attempt;
    if (!attempt) {
      try {
        attempt = prepareAliasWithdrawal(alias, () => globalThis.crypto.randomUUID());
      } catch {
        setState({
          client,
          scope,
          pending: false,
          error: {
            code: "WORKBENCH_ALIAS_WITHDRAWAL_UNAVAILABLE",
            message:
              "A stable withdrawal request could not be prepared. Reopen the alias before retrying.",
            retryable: false,
          },
        });
        return;
      }
    }
    lock.current = true;
    const request = generation.current;
    setState({ client, scope, pending: true, attempt });
    try {
      const result = await withdrawServiceAlias(client, attempt.alias, attempt.idempotencyKey);
      if (generation.current !== request) return;
      if (result.ok) {
        setState({ client, scope, pending: false, attempt, withdrawn: result.alias });
        onWithdrawn(result.alias);
      } else setState({ client, scope, pending: false, attempt, error: result.error });
    } finally {
      if (generation.current === request) lock.current = false;
    }
  }
  return (
    <section className="product-workflow__inspection" aria-labelledby={titleId}>
      <h2 id={titleId} ref={title} tabIndex={-1}>
        Withdraw alias {alias.name}
      </h2>
      <p>
        <code>{alias.serviceId}</code> · <code>{alias.metadata.id}</code> · Version{" "}
        {alias.metadata.version}
      </p>
      <p>
        This stops new invocations through this alias. Accepted work continues. The name can be
        assigned again after withdrawal.
      </p>
      {withdrawn ? (
        <div role="status" ref={feedback} tabIndex={-1}>
          <StatusBadge tone="success">Alias withdrawn</StatusBadge>
          <p>DataGround confirmed that {withdrawn.name} no longer accepts new invocations.</p>
        </div>
      ) : !canWithdraw ? (
        <p role="alert">Withdrawing this alias is unavailable with your current permissions.</p>
      ) : null}
      {problem ? (
        <div role="alert" className="workbench-inline-error" ref={feedback} tabIndex={-1}>
          <p>{problem.message}</p>
          {problem.correlationId ? (
            <p>
              Correlation: <code>{problem.correlationId}</code>
            </p>
          ) : null}
        </div>
      ) : null}
      {pending ? <p role="status">Confirming alias withdrawal…</p> : null}
      {!withdrawn && canWithdraw && (!problem || problem.retryable) ? (
        <Button isDisabled={pending || !current} onPress={() => void confirm()} variant="danger">
          {problem ? "Recover withdrawal request" : "Confirm alias withdrawal"}
        </Button>
      ) : null}
      <Button isDisabled={pending} onPress={onClose} variant="quiet">
        {withdrawn ? "Back to service" : "Close withdrawal"}
      </Button>
    </section>
  );
}
