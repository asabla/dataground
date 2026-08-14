import { Button, StatusBadge, type StatusTone } from "@dataground/ui";
import { useId } from "react";

export interface ArtifactMetadata {
  createdAt: string;
  createdBy: string;
  generation: number;
  id: string;
  isolationDomainId: string;
  provenance?: {
    requestCorrelationId?: string;
    sourceRevision?: string;
  };
  updatedAt: string;
  version: number;
}

export interface ArtifactResource {
  digest: string;
  invocationId: string;
  kind: string;
  mediaType: string;
  metadata: ArtifactMetadata;
  name: string;
  sensitive: boolean;
  sizeBytes: number;
  state: string;
}

export interface ArtifactReference {
  artifactId: string;
  invocationId: string;
  isolationDomainId: string;
}

export interface ArtifactCardError {
  correlationId?: string;
  message: string;
  retryable: boolean;
}

export interface ArtifactCardProps {
  artifact?: ArtifactResource;
  error?: ArtifactCardError;
  isLoading?: boolean;
  onRefresh?: () => void;
  reference: ArtifactReference;
}

interface ArtifactPresentation {
  label: string;
  tone: StatusTone;
}

const statePresentations: Record<string, ArtifactPresentation> = {
  available: { label: "Artifact available", tone: "success" },
  deleted: { label: "Artifact deleted", tone: "neutral" },
  failed: { label: "Artifact failed", tone: "critical" },
  pending: { label: "Artifact pending", tone: "waiting" },
};

const kindLabels: Record<string, string> = {
  file: "File",
  log: "Log",
  other: "Other",
  "event-payload": "Event payload",
  "structured-output": "Structured output",
};

function displayText(value: string, maximum = 255): string {
  const normalized = Array.from(value, (character) => {
    const codePoint = character.codePointAt(0) ?? 0;
    return codePoint <= 8 ||
      codePoint === 11 ||
      codePoint === 12 ||
      (codePoint >= 14 && codePoint <= 31) ||
      (codePoint >= 127 && codePoint <= 159)
      ? "�"
      : character;
  })
    .join("")
    .replaceAll(/\s+/gu, " ")
    .trim();
  return normalized.length > maximum ? `${normalized.slice(0, maximum)}…` : normalized;
}

function statePresentation(state: string): ArtifactPresentation {
  return (
    statePresentations[state] ?? {
      label: `Unknown state: ${displayText(state, 64)}`,
      tone: "neutral",
    }
  );
}

function formatBytes(value: number): string {
  const exact = new Intl.NumberFormat("en-US").format(value);
  if (value < 1024) {
    return `${exact} ${value === 1 ? "byte" : "bytes"}`;
  }
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let amount = value;
  let unit = units[0];
  for (const candidate of units) {
    amount /= 1024;
    unit = candidate;
    if (amount < 1024 || candidate === units.at(-1)) {
      break;
    }
  }
  return `${amount.toFixed(amount >= 10 ? 1 : 2)} ${unit} (${exact} bytes)`;
}

export function ArtifactCard({
  artifact,
  error,
  isLoading = false,
  onRefresh,
  reference,
}: ArtifactCardProps) {
  const titleId = useId();
  const canRefresh =
    onRefresh !== undefined && (!error || error.retryable || artifact !== undefined);
  const presentation = isLoading
    ? { label: artifact ? "Refreshing metadata" : "Loading metadata", tone: "active" as const }
    : error
      ? {
          label: artifact ? "Metadata degraded" : "Metadata unavailable",
          tone: artifact ? ("warning" as const) : ("critical" as const),
        }
      : artifact
        ? statePresentation(artifact.state)
        : { label: "Metadata unavailable", tone: "neutral" as const };

  return (
    <section
      aria-busy={isLoading || undefined}
      aria-labelledby={titleId}
      className="dg-artifact-card"
    >
      <div className="dg-artifact-card__heading">
        <div>
          <p className="dg-artifact-card__eyebrow">Invocation artifact</p>
          <h2 id={titleId}>
            {artifact ? displayText(artifact.name) || "Unnamed artifact" : "Artifact metadata"}
          </h2>
        </div>
        <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
      </div>

      <dl className="dg-artifact-card__scope">
        <div>
          <dt>Isolation domain</dt>
          <dd>{displayText(reference.isolationDomainId, 64)}</dd>
        </div>
        <div>
          <dt>Invocation</dt>
          <dd>{displayText(reference.invocationId, 64)}</dd>
        </div>
        <div>
          <dt>Artifact</dt>
          <dd>{displayText(reference.artifactId, 64)}</dd>
        </div>
      </dl>

      {error && (
        <div className="dg-artifact-card__error" role="alert">
          <strong>
            {artifact ? "Artifact metadata not refreshed." : "Artifact metadata unavailable."}
          </strong>{" "}
          {displayText(error.message, 512)}
          {error.correlationId && (
            <span>
              {" "}
              Correlation: <code>{displayText(error.correlationId, 128)}</code>
            </span>
          )}
        </div>
      )}

      {!artifact && !error && (
        <p aria-live="polite" className="dg-artifact-card__empty">
          {isLoading
            ? "Retrieving governed artifact metadata."
            : "No artifact metadata is available."}
        </p>
      )}

      {artifact && (
        <>
          {artifact.sensitive && (
            <p className="dg-artifact-card__sensitive">
              This artifact is marked sensitive. Only governed metadata is shown; content is not
              loaded into this surface.
            </p>
          )}

          <dl className="dg-artifact-card__facts">
            <div>
              <dt>State</dt>
              <dd>{displayText(artifact.state, 64)}</dd>
            </div>
            <div>
              <dt>Kind</dt>
              <dd>{kindLabels[artifact.kind] ?? `Unknown: ${displayText(artifact.kind, 64)}`}</dd>
            </div>
            <div>
              <dt>Media type</dt>
              <dd>{displayText(artifact.mediaType) || "Not reported"}</dd>
            </div>
            <div>
              <dt>Size</dt>
              <dd>{formatBytes(artifact.sizeBytes)}</dd>
            </div>
            <div>
              <dt>Marked sensitive</dt>
              <dd>{artifact.sensitive ? "Yes" : "No"}</dd>
            </div>
            <div>
              <dt>Generation / version</dt>
              <dd>
                {artifact.metadata.generation} / {artifact.metadata.version}
              </dd>
            </div>
            <div className="dg-artifact-card__digest">
              <dt>Content digest</dt>
              <dd>{artifact.digest}</dd>
            </div>
            <div>
              <dt>Created by</dt>
              <dd>{displayText(artifact.metadata.createdBy, 128) || "Not reported"}</dd>
            </div>
            <div>
              <dt>Created</dt>
              <dd>
                <time dateTime={artifact.metadata.createdAt}>{artifact.metadata.createdAt}</time>
              </dd>
            </div>
            <div>
              <dt>Last observed</dt>
              <dd>
                <time dateTime={artifact.metadata.updatedAt}>{artifact.metadata.updatedAt}</time>
              </dd>
            </div>
            {artifact.metadata.provenance?.sourceRevision && (
              <div>
                <dt>Source revision</dt>
                <dd>{displayText(artifact.metadata.provenance.sourceRevision, 128)}</dd>
              </div>
            )}
            {artifact.metadata.provenance?.requestCorrelationId && (
              <div>
                <dt>Request correlation</dt>
                <dd>{displayText(artifact.metadata.provenance.requestCorrelationId, 128)}</dd>
              </div>
            )}
          </dl>

          {!artifact.sensitive && (
            <p className="dg-artifact-card__boundary">
              This surface exposes metadata only. Artifact content delivery requires a separate
              governed object-store boundary.
            </p>
          )}
        </>
      )}

      {canRefresh && (
        <div className="dg-artifact-card__actions">
          <Button isDisabled={isLoading} onPress={onRefresh} variant="secondary">
            {isLoading
              ? "Refreshing metadata…"
              : error?.retryable
                ? "Retry metadata"
                : "Refresh metadata"}
          </Button>
        </div>
      )}
    </section>
  );
}
