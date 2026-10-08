import { Button } from "@dataground/ui";
import { useId } from "react";

export interface ArtifactContentProps {
  download?: { url: string; filename: string };
  error?: { message: string; correlationId?: string };
  isLoading?: boolean;
  onRead?: () => void;
  onHide?: () => void;
  previewMessage?: string;
  text?: string;
  unavailableMessage?: string;
}

export function ArtifactContent({
  download,
  error,
  isLoading = false,
  onRead,
  onHide,
  previewMessage,
  text,
  unavailableMessage,
}: ArtifactContentProps) {
  const titleId = useId();
  const contentId = useId();
  const shown = text !== undefined || download !== undefined;
  return (
    <section aria-labelledby={titleId} className="dg-artifact-content">
      <h2 id={titleId}>Artifact content</h2>
      <p role="status">
        {isLoading
          ? "Reading and verifying content…"
          : shown
            ? "Content verified."
            : "Content is not loaded."}
      </p>
      {unavailableMessage ? <p>{unavailableMessage}</p> : null}
      {error ? (
        <div role="alert">
          <p>{error.message}</p>
          {error.correlationId ? (
            <p>
              Correlation: <code>{error.correlationId}</code>
            </p>
          ) : null}
        </div>
      ) : null}
      {text !== undefined ? (
        <section
          id={contentId}
          aria-label="Artifact text"
          // biome-ignore lint/a11y/noNoninteractiveTabindex: This bounded text region needs keyboard focus for scrolling.
          tabIndex={0}
          className="dg-artifact-content__text"
          dir="ltr"
        >
          <pre>{text === "" ? "(Empty file)" : text}</pre>
        </section>
      ) : previewMessage ? (
        <p>{previewMessage}</p>
      ) : null}
      <div className="dg-artifact-content__actions">
        {!unavailableMessage || shown || isLoading ? (
          <Button
            onPress={shown || isLoading ? onHide : onRead}
            aria-controls={text !== undefined ? contentId : undefined}
          >
            {isLoading
              ? "Cancel content read"
              : shown
                ? "Hide content"
                : error
                  ? "Retry content read"
                  : "Read content"}
          </Button>
        ) : null}
        {download ? (
          <a
            className="dg-artifact-content__download"
            href={download.url}
            download={download.filename}
          >
            Download verified file
          </a>
        ) : null}
      </div>
    </section>
  );
}
