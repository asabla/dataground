import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { ServiceRevisionPublish } from "./ServiceRevisionPublish";

const props = {
  canPublish: true,
  createdAt: "2026-08-14T16:00:00Z",
  createdBy: "operator",
  hasInputSchema: true,
  hasOutputSchema: false,
  isolationDomainId: "iso_00000000000000000001",
  requiredCapabilities: ["tool", "usage"],
  revisionId: "rev_00000000000000000001",
  revisionNumber: 2,
  runtimeProfile: "reference/v1",
  serviceId: "svc_00000000000000000001",
  version: 1,
};

describe("ServiceRevisionPublish", () => {
  it("shows exact scope and immutable definition before offering publication review", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionPublish {...props} onRequestConfirmation={() => undefined} />,
    );

    assert.match(markup, /Publish revision/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /svc_00000000000000000001/u);
    assert.match(markup, /rev_00000000000000000001/u);
    assert.match(markup, /reference\/v1/u);
    assert.match(markup, /tool, usage/u);
    assert.match(markup, /Review publication/u);
    assert.doesNotMatch(markup, /Assign alias/u);
  });

  it("requires a distinct confirmation step and keeps alias routing separate", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionPublish
        {...props}
        confirmationVisible
        onConfirm={() => undefined}
        onDismissConfirmation={() => undefined}
      />,
    );

    assert.match(markup, /Confirm revision publication/u);
    assert.match(markup, /Confirm publication/u);
    assert.match(markup, /Keep as draft/u);
    assert.match(markup, /does not assign or move an alias/u);
    assert.doesNotMatch(markup, /Review publication/u);
  });

  it("fails closed for observers without hiding the target revision", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionPublish
        {...props}
        canPublish={false}
        disabledReason="Only service publishers may publish revisions."
      />,
    );

    assert.match(markup, /Observer access only/u);
    assert.match(markup, /rev_00000000000000000001/u);
    assert.match(markup, /disabled/u);
  });

  it("offers exact-request recovery after an uncertain outcome", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionPublish
        {...props}
        error={{ message: "The response could not be confirmed.", retryable: true }}
        onConfirm={() => undefined}
        recoveryPending
      />,
    );

    assert.match(markup, /Retry publication/u);
    assert.match(markup, /original expected version and request identifier/u);
    assert.doesNotMatch(markup, /Confirm revision publication/u);
  });

  it("blocks a new request when an uncertain outcome cannot be retried safely", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionPublish
        {...props}
        error={{
          message: "The accepted operation was not bound to this revision.",
          outcomeUnknown: true,
          retryable: false,
        }}
      />,
    );

    assert.match(markup, /Do not submit another publication request/u);
    assert.match(markup, /authorized API or operator view/u);
    assert.doesNotMatch(markup, /Review publication|Retry publication/u);
  });

  it("presents publication without implying that the revision is routable", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionPublish
        {...props}
        onAssignAlias={() => undefined}
        published={{ publishedAt: "2026-08-14T16:01:00Z", version: 2 }}
      />,
    );

    assert.match(markup, /published the immutable revision/u);
    assert.match(markup, /does not make the revision routable/u);
    assert.match(markup, /Assign alias/u);
    assert.doesNotMatch(markup, /Review publication/u);
  });
});
