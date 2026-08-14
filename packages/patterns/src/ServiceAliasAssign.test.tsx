import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { ServiceAliasAssign } from "./ServiceAliasAssign";

const props = {
  aliasName: "stable",
  canAssign: true,
  isolationDomainId: "iso_00000000000000000001",
  revisionNumber: 2,
  serviceId: "svc_00000000000000000001",
  targetRevisionId: "rev_00000000000000000001",
  targetVersion: 2,
};

describe("ServiceAliasAssign", () => {
  it("shows exact scope and a zero-version precondition for a new alias", () => {
    const markup = renderToStaticMarkup(
      <ServiceAliasAssign
        {...props}
        onAliasNameChange={() => undefined}
        onRequestConfirmation={() => undefined}
      />,
    );

    assert.match(markup, /Assign service alias/u);
    assert.match(markup, /iso_00000000000000000001/u);
    assert.match(markup, /svc_00000000000000000001/u);
    assert.match(markup, /rev_00000000000000000001/u);
    assert.match(markup, /Expected alias version/u);
    assert.match(markup, />0<\/dd>/u);
    assert.match(markup, /Review routing/u);
  });

  it("requires confirmation before moving observed traffic", () => {
    const markup = renderToStaticMarkup(
      <ServiceAliasAssign
        {...props}
        confirmationVisible
        currentAliasId="als_00000000000000000001"
        currentRevisionId="rev_00000000000000000002"
        currentVersion={4}
        onConfirm={() => undefined}
        onDismissConfirmation={() => undefined}
      />,
    );

    assert.match(markup, /Move service alias/u);
    assert.match(markup, /Confirm service route change/u);
    assert.match(markup, /observed alias version/u);
    assert.match(markup, /Existing invocations remain pinned/u);
    assert.match(markup, /Confirm route change/u);
    assert.match(markup, /Keep current route/u);
  });

  it("fails closed for observers without hiding routing scope", () => {
    const markup = renderToStaticMarkup(
      <ServiceAliasAssign
        {...props}
        canAssign={false}
        disabledReason="Only service routers may assign aliases."
      />,
    );

    assert.match(markup, /Observer access only/u);
    assert.match(markup, /rev_00000000000000000001/u);
    assert.match(markup, /disabled/u);
  });

  it("offers exact-request recovery after an uncertain outcome", () => {
    const markup = renderToStaticMarkup(
      <ServiceAliasAssign
        {...props}
        error={{
          message: "The response could not be confirmed.",
          retryable: true,
        }}
        onConfirm={() => undefined}
        recoveryPending
      />,
    );

    assert.match(markup, /Retry route change/u);
    assert.match(markup, /original alias, target revision, expected version/u);
    assert.doesNotMatch(markup, /Review routing|Confirm service route change/u);
  });

  it("requires refreshed observed state after a terminal routing failure", () => {
    const markup = renderToStaticMarkup(
      <ServiceAliasAssign
        {...props}
        error={{ message: "Alias version did not match.", retryable: false }}
      />,
    );

    assert.match(markup, /Refresh the published revision and current alias state/u);
    assert.doesNotMatch(markup, /Review routing|Retry route change/u);
  });

  it("does not mutate an alias that already targets the revision", () => {
    const markup = renderToStaticMarkup(
      <ServiceAliasAssign
        {...props}
        currentAliasId="als_00000000000000000001"
        currentRevisionId={props.targetRevisionId}
        currentVersion={5}
        onComposeInvocation={() => undefined}
      />,
    );

    assert.match(markup, /Already routed/u);
    assert.match(markup, /No routing command is needed/u);
    assert.match(markup, /Compose invocation/u);
    assert.doesNotMatch(markup, /Review routing/u);
  });

  it("presents successful routing without implying existing work moved", () => {
    const markup = renderToStaticMarkup(
      <ServiceAliasAssign
        {...props}
        assigned={{
          generation: 1,
          id: "als_00000000000000000001",
          name: "stable",
          revisionId: props.targetRevisionId,
          updatedAt: "2026-08-14T16:02:00Z",
          version: 1,
        }}
        onComposeInvocation={() => undefined}
      />,
    );

    assert.match(markup, /routed the alias to the published revision/u);
    assert.match(markup, /Existing invocations remain revision-pinned/u);
    assert.match(markup, /Compose invocation/u);
    assert.doesNotMatch(markup, /Review routing/u);
  });
});
