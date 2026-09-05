import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { it } from "vitest";
import { QuestionRequest, type QuestionRequestProps } from "./QuestionRequest";

const props: QuestionRequestProps = {
  question: {
    id: "qst_00000000000000000001",
    invocationId: "inv_00000000000000000001",
    isolationDomainId: "iso_00000000000000000001",
    state: "pending",
    expiresAt: "2026-09-06T12:00:00Z",
    questions: [
      {
        id: "item_1",
        title: "Target",
        prompt: "<img src=x onerror=alert(1)>",
        allowFreeText: true,
        multiple: false,
      },
    ],
  },
  canAnswer: true,
  complete: false,
  expired: false,
  drafts: {},
  onDraft: () => {},
  onSubmit: () => {},
  onRefresh: () => {},
};
it("escapes runtime text and requires an explicit answer", () => {
  const html = renderToStaticMarkup(<QuestionRequest {...props} />);
  assert.match(html, /&lt;img/u);
  assert.doesNotMatch(html, /<img/u);
  assert.match(html, /<fieldset/u);
  assert.match(html, /<legend>Target/u);
  assert.match(html, /disabled=""[^>]*>Submit answers/u);
});
it("keeps observer, expiry and uncertain delivery distinct", () => {
  const observer = renderToStaticMarkup(<QuestionRequest {...props} canAnswer={false} />);
  assert.doesNotMatch(observer, /textarea|Submit answers/u);
  assert.match(observer, /cannot submit answers/u);
  const expired = renderToStaticMarkup(<QuestionRequest {...props} expired />);
  assert.match(expired, /deadline has passed/u);
  assert.doesNotMatch(expired, /Submit answers/u);
  const unknown = renderToStaticMarkup(
    <QuestionRequest {...props} question={{ ...props.question, state: "delivery_unknown" }} />,
  );
  assert.match(unknown, /will not repeat delivery/u);
  assert.doesNotMatch(unknown, /Submit answers/u);
});
