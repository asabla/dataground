import { Button, StatusBadge } from "@dataground/ui";
import { useId } from "react";

export interface InvocationResultProps {
  error?: { message: string; correlationId?: string };
  isLoading?: boolean;
  onHide?: () => void;
  onShow?: () => void;
  text?: string;
}

export function InvocationResult({
  error,
  isLoading = false,
  onHide,
  onShow,
  text,
}: InvocationResultProps) {
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} className="dg-invocation-result">
      <h2 id={headingId}>Invocation result</h2>
      {isLoading ? <StatusBadge tone="active">Loading result</StatusBadge> : null}
      {error ? (
        <div role="alert">
          <p>{error.message}</p>
          {error.correlationId ? <p>Correlation: {error.correlationId}</p> : null}
        </div>
      ) : null}
      {text !== undefined ? (
        <section
          aria-label="Invocation result JSON"
          // biome-ignore lint/a11y/noNoninteractiveTabindex: This bounded result region needs keyboard focus for scrolling.
          tabIndex={0}
          className="dg-invocation-result__content"
        >
          <pre>{text}</pre>
        </section>
      ) : (
        <p>Show the completed result using your current access.</p>
      )}
      <div className="dg-invocation-result__actions">
        <Button onPress={text !== undefined || isLoading ? onHide : onShow}>
          {text !== undefined || isLoading
            ? "Hide result"
            : error
              ? "Retry result read"
              : "Show result"}
        </Button>
      </div>
    </section>
  );
}
