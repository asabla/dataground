import { useState } from "react";
import { ServiceAliasAssignWorkflow, type ServiceAliasResource } from "../aliases";
import type { DataGroundClient } from "../contracts/client";
import { InvocationComposerWorkflow, type InvocationReference } from "../invocations";
import {
  type PublishedServiceRevisionResource,
  ServiceRevisionPublishWorkflow,
  type ServiceRevisionResource,
} from "../revisions";
import {
  type AgentServiceResource,
  isAliasSelectedForPublishedRevision,
  isPublishedRevisionSelectedForService,
  isRevisionSelectedForService,
} from "../services";

type CommandKind = "publication" | "route" | "invocation";
interface CommandSelection {
  key: string;
  revision: ServiceRevisionResource;
  publication?: PublishedServiceRevisionResource;
  alias?: ServiceAliasResource;
  observedAlias?: ServiceAliasResource;
}
interface RetainedServiceCommandsProps {
  kind: CommandKind;
  client: DataGroundClient;
  service: AgentServiceResource;
  isolationDomainId: string;
  enabled: boolean;
  active?: boolean;
  revision?: ServiceRevisionResource;
  publication?: PublishedServiceRevisionResource;
  alias?: ServiceAliasResource;
  observedAlias?: ServiceAliasResource;
  onAssignAlias: (revision: PublishedServiceRevisionResource) => void;
  onPublished: (revision: PublishedServiceRevisionResource) => void;
  onAliasAssigned?: (alias: ServiceAliasResource) => void;
  onComposeInvocation: (alias: ServiceAliasResource) => void;
  onInvocationCreated?: (reference: InvocationReference) => void;
  onOpenInvocation: (reference: InvocationReference) => void;
}

function selection(props: RetainedServiceCommandsProps): CommandSelection | undefined {
  const { kind, enabled, revision, publication, alias, observedAlias, service, isolationDomainId } =
    props;
  if (!enabled || !revision || !isRevisionSelectedForService(revision, service, isolationDomainId))
    return;
  if (
    kind !== "publication" &&
    (!publication ||
      !isPublishedRevisionSelectedForService(publication, revision, service, isolationDomainId))
  )
    return;
  if (
    kind === "invocation" &&
    (!alias ||
      !publication ||
      !isAliasSelectedForPublishedRevision(
        alias,
        publication,
        revision,
        service,
        isolationDomainId,
      ))
  )
    return;
  const versionKey = (
    resource:
      | { metadata: { id: string; version: number; generation: number; updatedAt: string } }
      | undefined,
  ) =>
    resource
      ? `${resource.metadata.id}/${resource.metadata.generation}/${resource.metadata.version}/${resource.metadata.updatedAt}`
      : "none";
  return {
    key: [
      kind,
      versionKey(revision),
      kind !== "publication" ? versionKey(publication) : "",
      kind === "route" ? versionKey(observedAlias) : "",
      kind === "invocation" ? versionKey(alias) : "",
    ].join(":"),
    revision,
    ...(publication ? { publication } : {}),
    ...(alias ? { alias } : {}),
    ...(observedAlias ? { observedAlias } : {}),
  };
}

export function RetainedServiceCommands(props: RetainedServiceCommandsProps) {
  const scope = `${props.isolationDomainId}/${props.service.metadata.id}/${props.kind}`;
  const [identity, setIdentity] = useState({ client: props.client, scope, generation: 0 });
  if (identity.client !== props.client || identity.scope !== scope) {
    setIdentity({ client: props.client, scope, generation: identity.generation + 1 });
  }
  return <CommandSessions key={identity.generation} {...props} />;
}

function CommandSessions(props: RetainedServiceCommandsProps) {
  const current = selection(props);
  const [opened, setOpened] = useState<CommandSelection[]>([]);
  if (current && !opened.some((entry) => entry.key === current.key)) {
    // Freeze command inputs, not elements or callbacks. A later discovery must
    // not replace the preconditions of an uncertain request in another session.
    setOpened([...opened, structuredClone(current)]);
  }
  return (
    <>
      {opened.map((entry) => (
        <div key={entry.key} hidden={entry.key !== current?.key} inert={entry.key !== current?.key}>
          {props.kind === "publication" && (
            <ServiceRevisionPublishWorkflow
              compact
              active={props.active !== false && entry.key === current?.key}
              canPublish
              client={props.client}
              revision={entry.revision}
              onAssignAlias={props.onAssignAlias}
              onPublished={props.onPublished}
            />
          )}
          {props.kind === "route" && entry.publication && (
            <ServiceAliasAssignWorkflow
              compact
              active={props.active !== false && entry.key === current?.key}
              canAssign
              client={props.client}
              revision={entry.publication}
              currentAlias={entry.observedAlias}
              onAliasAssigned={props.onAliasAssigned}
              onComposeInvocation={props.onComposeInvocation}
            />
          )}
          {props.kind === "invocation" && entry.publication && entry.alias && (
            <InvocationComposerWorkflow
              compact
              canInvoke
              client={props.client}
              target={{
                isolationDomainId: props.isolationDomainId,
                serviceId: props.service.metadata.id,
              }}
              inputSchema={entry.publication.inputSchema}
              initialAlias={entry.alias.name}
              onInvocationCreated={props.onInvocationCreated}
              onOpenInvocation={props.onOpenInvocation}
            />
          )}
        </div>
      ))}
    </>
  );
}
