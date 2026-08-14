import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { ServiceRevisionDraft } from "./ServiceRevisionDraft";

const props = {
  canCreateRevision: true,
  inputSchema: '{"type":"object"}',
  isolationDomainId: "iso_00000000000000000001",
  outputSchema: "",
  requiredCapabilities: "tool, usage",
  runtimeProfile: "reference/v1",
  serviceId: "svc_00000000000000000001",
};

describe("ServiceRevisionDraft", () => {
  it("shows exact scope and revision definition fields without publication controls", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionDraft {...props} onSubmit={() => undefined} />,
    );

    assert.match(markup, /Create revision draft/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /svc_00000000000000000001/u);
    assert.match(markup, /Runtime profile/u);
    assert.match(markup, /Required capabilities/u);
    assert.match(markup, /Input schema/u);
    assert.match(markup, /Output schema/u);
    assert.doesNotMatch(markup, /Publish revision|Assign alias|Provider credential/u);
  });

  it("fails closed for observers without hiding the target scope", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionDraft
        {...props}
        canCreateRevision={false}
        disabledReason="Only service operators may create revisions."
      />,
    );

    assert.match(markup, /Observer access only/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /svc_00000000000000000001/u);
    assert.match(markup, /disabled/u);
  });

  it("offers exact-request recovery without echoing retained definitions", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionDraft
        {...props}
        error={{ message: "The response could not be confirmed.", retryable: true }}
        onSubmit={() => undefined}
        recoveryPending
      />,
    );

    assert.match(markup, /Retry revision creation/u);
    assert.match(markup, /original request identifier/u);
    assert.doesNotMatch(markup, /reference\/v1|tool, usage|\{&quot;type&quot;/u);
  });

  it("presents a created resource as an unpublished draft", () => {
    const markup = renderToStaticMarkup(
      <ServiceRevisionDraft
        {...props}
        created={{
          createdAt: "2026-08-14T16:00:00Z",
          createdBy: "operator",
          id: "rev_00000000000000000001",
          revisionNumber: 2,
          runtimeProfile: "reference/v1",
          state: "draft",
          version: 1,
        }}
      />,
    );

    assert.match(markup, /unpublished revision draft/u);
    assert.match(markup, /rev_00000000000000000001/u);
    assert.match(markup, /not published, routable, or invocable/u);
    assert.doesNotMatch(markup, />Create revision draft<\/button>/u);
  });
});
