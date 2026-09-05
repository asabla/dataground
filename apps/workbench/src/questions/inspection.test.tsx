import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import {
  InvocationInspectionWorkflow,
  isQuestionSelectedForInvocation,
} from "../invocations/InvocationInspectionWorkflow";
import { questionReference as reference } from "./fixtures";

it("blocks cross-invocation questions before rendering the inspection workflow", () => {
  assert.equal(isQuestionSelectedForInvocation(reference, reference), true);
  for (const question of [
    { ...reference, questionId: "native-id" },
    { ...reference, invocationId: "inv_00000000000000000002" },
    { ...reference, isolationDomainId: "iso_00000000000000000002" },
  ]) {
    assert.equal(isQuestionSelectedForInvocation(question, reference), false);
    const markup = renderToStaticMarkup(
      <InvocationInspectionWorkflow
        client={{} as DataGroundClient}
        reference={reference}
        selectedQuestion={question}
        canAnswerQuestion
        canCancelInvocation={false}
        canResolveApproval={false}
        onCloseApproval={() => {}}
        onCloseArtifact={() => {}}
        onInspectApproval={() => {}}
        onInspectArtifact={() => {}}
      />,
    );
    assert.match(markup, /selected question does not belong/u);
    assert.doesNotMatch(markup, /Loading question|Submit answers/u);
  }
});
