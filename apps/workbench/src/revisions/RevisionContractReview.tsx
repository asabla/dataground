import { Button } from "@dataground/ui";
import { useEffect, useId, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import { normalizeInvocationComposerSchema } from "../invocations/composerSchema";
import type { ServiceRevisionResource } from "./client";
import { prepareContractPreview } from "./contractReview";

interface RevisionContractReviewProps {
  client: DataGroundClient;
  revision: ServiceRevisionResource;
}

function ContractContent({ name, schema }: { name: string; schema?: Record<string, unknown> }) {
  const preview = prepareContractPreview(schema);
  return (
    <div className="revision-contract-review__contract">
      <h3>{name} contract</h3>
      {preview.state === "absent" ? (
        <p>No {name.toLowerCase()} contract is declared.</p>
      ) : preview.state === "unavailable" ? (
        <p role="alert">
          The complete contract cannot be displayed within the review limit. Inspect it through an
          authorized API client before publication.
        </p>
      ) : (
        <section
          aria-label={`${name} contract JSON`}
          className="revision-contract-review__content"
          // biome-ignore lint/a11y/noNoninteractiveTabindex: The bounded JSON region needs keyboard focus for scrolling.
          tabIndex={0}
        >
          <pre>{preview.text}</pre>
        </section>
      )}
    </div>
  );
}

export function RevisionContractReview({ client, revision }: RevisionContractReviewProps) {
  const contentId = useId();
  const [revealed, setRevealed] = useState<RevisionContractReviewProps>();
  // Bind disclosure to both the exact loaded object and connection. Scope or
  // identity changes must hide old content in the render before effects run.
  const visible = revealed?.client === client && revealed.revision === revision;
  useEffect(() => {
    setRevealed((current) =>
      current?.client === client && current.revision === revision ? current : undefined,
    );
  }, [client, revision]);
  return (
    <div className="revision-contract-review">
      <p>Inspect the loaded input and output contracts before making this revision immutable.</p>
      <Button
        aria-expanded={visible}
        aria-controls={contentId}
        variant="quiet"
        onPress={() => setRevealed(visible ? undefined : { client, revision })}
      >
        {visible ? "Hide revision contracts" : "Inspect revision contracts"}
      </Button>
      <div id={contentId}>
        {visible && (
          <>
            <p>
              Revision <code>{revision.metadata.id}</code>, version {revision.metadata.version}.
            </p>
            <p>
              {normalizeInvocationComposerSchema(revision.inputSchema).ok
                ? "The workbench can render this input contract as an interaction form. Publication validation is still required."
                : "The workbench cannot render this input contract as an interaction form. Publication validates the contract separately."}
            </p>
            <ContractContent name="Input" schema={revision.inputSchema} />
            <ContractContent name="Output" schema={revision.outputSchema} />
          </>
        )}
      </div>
    </div>
  );
}
