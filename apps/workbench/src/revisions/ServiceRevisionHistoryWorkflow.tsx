import { Button } from "@dataground/ui";
import { useEffect, useId, useRef, useState } from "react";
import { validResourceAuditReference } from "../audit/client";
import { ResourceAuditWorkflow } from "../audit/ResourceAuditWorkflow";
import type { DataGroundClient } from "../contracts/client";
import {
  ServiceRevisionHistoryPanel,
  type ServiceRevisionHistoryPanelProps,
} from "./ServiceRevisionHistoryPanel";

export function ServiceRevisionHistoryWorkflow({
  client,
  isolationDomainId,
  serviceId,
  titleId: suppliedTitleId,
  ...history
}: Omit<ServiceRevisionHistoryPanelProps, "onInspectAudit"> & {
  client: DataGroundClient;
  isolationDomainId: string;
  serviceId: string;
}) {
  const generatedTitleId = useId();
  const titleId = suppliedTitleId ?? generatedTitleId;
  const scope = `${isolationDomainId}/${serviceId}`;
  const inspection = useRef<HTMLElement>(null);
  const [selected, setSelected] = useState<{
    client: DataGroundClient;
    scope: string;
    id: string;
  }>();
  useEffect(() => {
    setSelected((previous) =>
      previous?.client === client && previous.scope === scope ? previous : undefined,
    );
  }, [client, scope]);
  const visible =
    selected?.client === client &&
    selected.scope === scope &&
    !history.error &&
    !history.isLoading &&
    history.revisions.some(
      (revision) =>
        revision.metadata.id === selected.id &&
        revision.metadata.isolationDomainId === isolationDomainId &&
        revision.serviceId === serviceId,
    )
      ? selected
      : undefined;
  useEffect(() => {
    if (visible) inspection.current?.focus();
  }, [visible]);
  return (
    <>
      <ServiceRevisionHistoryPanel
        {...history}
        titleId={titleId}
        onInspectAudit={(revision) => {
          const reference = {
            isolationDomainId,
            resourceType: "service-revision" as const,
            resourceId: revision.metadata.id,
          };
          if (
            validResourceAuditReference(reference) &&
            revision.metadata.isolationDomainId === isolationDomainId &&
            revision.serviceId === serviceId &&
            history.revisions.includes(revision)
          ) {
            setSelected({ client, scope, id: revision.metadata.id });
          }
        }}
      />
      {visible && (
        <section aria-label="Revision audit inspection" ref={inspection} tabIndex={-1}>
          <Button
            variant="quiet"
            onPress={() => {
              setSelected(undefined);
              document.getElementById(titleId)?.focus();
            }}
          >
            Close revision audit
          </Button>
          <ResourceAuditWorkflow
            key={`${scope}/${visible.id}`}
            client={client}
            reference={{
              isolationDomainId,
              resourceType: "service-revision",
              resourceId: visible.id,
            }}
          />
        </section>
      )}
    </>
  );
}
