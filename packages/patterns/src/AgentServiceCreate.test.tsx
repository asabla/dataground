import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { AgentServiceCreate } from "./AgentServiceCreate";

const props = {
  canCreate: true,
  description: "A governed service.",
  isolationDomainId: "iso_00000000000000000001",
  name: "Reference service",
};

describe("AgentServiceCreate", () => {
  it("shows explicit scope and bounded product fields without runtime controls", () => {
    const markup = renderToStaticMarkup(
      <AgentServiceCreate {...props} onSubmit={() => undefined} />,
    );

    assert.match(markup, /Create agent service/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /Service name/u);
    assert.match(markup, /Description/u);
    assert.match(markup, /Create service/u);
    assert.doesNotMatch(markup, /Runtime profile|Sandbox endpoint|Provider credential/u);
  });

  it("fails closed for observers without hiding the target scope", () => {
    const markup = renderToStaticMarkup(
      <AgentServiceCreate
        {...props}
        canCreate={false}
        disabledReason="Only service operators may create services."
      />,
    );

    assert.match(markup, /Observer access only/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /disabled/u);
  });

  it("offers only exact-request recovery without echoing retained fields", () => {
    const markup = renderToStaticMarkup(
      <AgentServiceCreate
        {...props}
        error={{ message: "The response could not be confirmed.", retryable: true }}
        onSubmit={() => undefined}
        recoveryPending
      />,
    );

    assert.match(markup, /Retry service creation/u);
    assert.match(markup, /original request identifier/u);
    assert.doesNotMatch(markup, /Reference service|A governed service/u);
  });

  it("shows governed resource identity after creation without another create action", () => {
    const markup = renderToStaticMarkup(
      <AgentServiceCreate
        {...props}
        created={{
          createdAt: "2026-08-14T12:00:00Z",
          createdBy: "operator",
          id: "svc_00000000000000000001",
          name: "Reference service",
          version: 1,
        }}
      />,
    );

    assert.match(markup, /DataGround created the service/u);
    assert.match(markup, /svc_00000000000000000001/u);
    assert.doesNotMatch(markup, /Create service/u);
  });
});
