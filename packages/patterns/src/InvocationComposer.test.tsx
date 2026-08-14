import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { InvocationComposer } from "./InvocationComposer";

const props = {
  alias: "stable",
  canInvoke: true,
  schema: {
    description: "Supply the governed prompt.",
    fields: [{ key: "prompt", label: "Prompt", maxLength: 262_144, minLength: 1, required: true }],
    title: "Agent prompt",
  },
  target: {
    isolationDomainId: "iso_00000000000000000001",
    serviceId: "svc_00000000000000000001",
  },
  values: { prompt: "Investigate the incident." },
};

describe("InvocationComposer", () => {
  it("keeps scope and governed fields visible without runtime controls", () => {
    const markup = renderToStaticMarkup(
      <InvocationComposer {...props} onSubmit={() => undefined} />,
    );

    assert.match(markup, /Agent prompt/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /svc_00000000000000000001/u);
    assert.match(markup, /Alias/u);
    assert.match(markup, /Prompt/u);
    assert.match(markup, /Start invocation/u);
    assert.doesNotMatch(markup, /model|provider|sandbox/iu);
  });

  it("fails closed for observers and unsupported contracts", () => {
    const markup = renderToStaticMarkup(
      <InvocationComposer
        {...props}
        canInvoke={false}
        disabledReason="Your role cannot create invocations."
        schema={undefined}
        schemaError="This input contract is unsupported."
      />,
    );

    assert.match(markup, /Observer access only/u);
    assert.match(markup, /Input contract unavailable/u);
    assert.doesNotMatch(markup, /Start invocation/u);
  });

  it("shows same-request recovery without echoing submitted input", () => {
    const markup = renderToStaticMarkup(
      <InvocationComposer
        {...props}
        error={{ message: "The response could not be confirmed.", retryable: true }}
        onSubmit={() => undefined}
        recoveryPending
        values={{ prompt: "secret submitted prompt" }}
      />,
    );

    assert.match(markup, /Retry invocation/u);
    assert.match(markup, /original request identifier/u);
    assert.doesNotMatch(markup, /secret submitted prompt/u);
  });

  it("shows only governed references after acceptance", () => {
    const markup = renderToStaticMarkup(
      <InvocationComposer
        {...props}
        accepted={{
          invocationId: "inv_00000000000000000001",
          operationId: "op_00000000000000000001",
          state: "accepted",
        }}
      />,
    );

    assert.match(markup, /DataGround accepted/u);
    assert.match(markup, /inv_00000000000000000001/u);
    assert.doesNotMatch(markup, /Investigate the incident/u);
    assert.doesNotMatch(markup, /Start invocation/u);
  });
});
