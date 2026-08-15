import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { App, DevelopmentWorkbench, validateDevelopmentScope } from "./App";
import type { DataGroundClient } from "./contracts/client";

describe("App", () => {
  it("renders a development connection boundary without exposing product commands", () => {
    const markup = renderToStaticMarkup(<App />);

    assert.match(markup, /<main/u);
    assert.match(markup, /Connect to the reference API/u);
    assert.match(markup, /Development only/u);
    assert.match(markup, /type="password"/u);
    assert.match(markup, /Open development scope/u);
    assert.doesNotMatch(markup, /Create agent service/u);
  });

  it("validates the exact isolation-domain shape and bearer transport bounds", () => {
    assert.deepEqual(validateDevelopmentScope("bad-domain", "a".repeat(32)), {
      isolationDomainId:
        "Use an isolation domain identifier beginning with iso_ and 20 to 32 lowercase letters or digits.",
    });
    assert.deepEqual(validateDevelopmentScope("iso_00000000000000000001", "a".repeat(31)), {
      bearerToken: "The development bearer token must be at least 32 bytes.",
    });
    assert.deepEqual(validateDevelopmentScope("iso_00000000000000000001", `${"a".repeat(31)} `), {
      bearerToken: "The token contains characters that cannot be sent in a Bearer header.",
    });
    assert.deepEqual(validateDevelopmentScope("iso_00000000000000000001", "a".repeat(8193)), {
      bearerToken: "The development bearer token must not exceed 8,192 bytes.",
    });
    assert.deepEqual(validateDevelopmentScope(" iso_00000000000000000001 ", "a".repeat(32)), {});
  });

  it("renders the connected workbench with explicit scope and honest empty states", () => {
    const markup = renderToStaticMarkup(
      <DevelopmentWorkbench
        client={{} as DataGroundClient}
        isolationDomainId="iso_00000000000000000001"
        onDisconnect={() => undefined}
      />,
    );

    assert.match(markup, /Agent services/u);
    assert.match(markup, /Interactions/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /No services opened in this session/u);
    assert.match(markup, /service-list contract is not available yet/u);
    assert.doesNotMatch(markup, /Create agent service<\/h2>/u);
  });
});
