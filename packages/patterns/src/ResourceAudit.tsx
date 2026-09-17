import { Button, StatusBadge } from "@dataground/ui";
import { useId, useRef } from "react";

export interface AuditRecordSummary {
  id: string;
  source: "lifecycle" | "invocation-authorization" | "publication-authorization";
  recordedAt: string;
  actorId: string;
  action: string;
  outcome: string;
  correlationId: string;
  operationId?: string;
  policySetId?: string;
  policyDigest?: string;
  phase?: "entry" | "effect";
}
export interface ResourceAuditProps {
  title: string;
  resourceId: string;
  items?: AuditRecordSummary[];
  receiptId?: string;
  isLoading?: boolean;
  hasNextPage?: boolean;
  error?: { message: string; correlationId?: string };
  onRead?: () => void;
  onNext?: () => void;
  onHide?: () => void;
}
const sourceLabels = {
  lifecycle: "Lifecycle",
  "invocation-authorization": "Invocation authorization",
  "publication-authorization": "Publication authorization",
};
export function ResourceAudit({
  title,
  resourceId,
  items,
  receiptId,
  isLoading = false,
  hasNextPage = false,
  error,
  onRead,
  onNext,
  onHide,
}: ResourceAuditProps) {
  const titleId = useId();
  const controls = useRef<HTMLDivElement>(null);
  const visible = items !== undefined || isLoading;
  return (
    <section aria-labelledby={titleId} className="dg-resource-audit">
      <h2 id={titleId}>{title}</h2>
      <p>
        Resource: <code>{resourceId}</code>
      </p>
      <p>
        Read lifecycle and policy decision records using your current access. Pages keep the
        original snapshot. Refresh to include later records.
      </p>
      <div ref={controls} className="dg-resource-audit__actions">
        <Button onPress={visible ? onHide : onRead}>
          {visible ? "Hide audit" : error ? "Retry audit read" : "Show audit"}
        </Button>
        {items !== undefined && (
          <Button
            onPress={() => {
              controls.current?.querySelector("button")?.focus();
              onRead?.();
            }}
            variant="quiet"
          >
            Refresh audit
          </Button>
        )}
        {items !== undefined && hasNextPage && (
          <Button
            onPress={() => {
              controls.current?.querySelector("button")?.focus();
              onNext?.();
            }}
            variant="quiet"
          >
            Next audit page
          </Button>
        )}
      </div>
      {isLoading && <p role="status">Loading audit records…</p>}
      {error && (
        <div role="alert">
          <p>{error.message}</p>
          {error.correlationId && (
            <p>
              Correlation: <code>{error.correlationId}</code>
            </p>
          )}
        </div>
      )}
      {items !== undefined && (
        <>
          <p role="status">
            {items.length === 0
              ? "No audit records in this snapshot."
              : `${items.length} ${items.length === 1 ? "record" : "records"} on this page.`}
          </p>
          <p>
            Read receipt: <code>{receiptId}</code>
          </p>
          <p>Lifecycle records appear first, then policy decisions. This is not timestamp order.</p>
          <ol className="dg-resource-audit__records">
            {items.map((item) => (
              <li key={item.id}>
                <h3>{sourceLabels[item.source]}</h3>
                <StatusBadge
                  tone={
                    item.outcome === "allowed" || item.outcome === "succeeded"
                      ? "success"
                      : item.outcome === "denied" || item.outcome === "failed"
                        ? "critical"
                        : "neutral"
                  }
                >
                  {item.outcome}
                </StatusBadge>
                <dl>
                  <div>
                    <dt>Record</dt>
                    <dd>
                      <code>{item.id}</code>
                    </dd>
                  </div>
                  <div>
                    <dt>Time</dt>
                    <dd>
                      <time dateTime={item.recordedAt}>{item.recordedAt}</time>
                    </dd>
                  </div>
                  <div>
                    <dt>Actor</dt>
                    <dd>{item.actorId}</dd>
                  </div>
                  <div>
                    <dt>Action</dt>
                    <dd>{item.action}</dd>
                  </div>
                  <div>
                    <dt>Correlation</dt>
                    <dd>
                      <code>{item.correlationId}</code>
                    </dd>
                  </div>
                  {item.operationId && (
                    <div>
                      <dt>Operation</dt>
                      <dd>
                        <code>{item.operationId}</code>
                      </dd>
                    </div>
                  )}
                  {item.policySetId && (
                    <div>
                      <dt>Policy set</dt>
                      <dd>{item.policySetId}</dd>
                    </div>
                  )}
                  {item.policyDigest && (
                    <div>
                      <dt>Policy digest</dt>
                      <dd>
                        <code>{item.policyDigest}</code>
                      </dd>
                    </div>
                  )}
                  {item.phase && (
                    <div>
                      <dt>Publication phase</dt>
                      <dd>{item.phase}</dd>
                    </div>
                  )}
                </dl>
              </li>
            ))}
          </ol>
          {!hasNextPage && items.length > 0 && <p>End of this audit snapshot.</p>}
        </>
      )}
    </section>
  );
}
