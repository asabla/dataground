import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import {
  answerInvocationQuestion,
  matchesQuestion,
  readInvocationQuestion,
  sameQuestionRequest,
  validQuestionAnswers,
  validQuestionPrompts,
} from "./client";
import { answeredQuestion, pendingQuestion, questionReference as reference } from "./fixtures";

describe("question client", () => {
  it("compares immutable prompts independently of JSON object key order", () => {
    const question = pendingQuestion();
    const reordered = {
      ...question,
      questions: question.questions.map((prompt) => ({
        options: prompt.options?.map((option) => ({
          description: option.description,
          label: option.label,
          id: option.id,
        })),
        allowFreeText: prompt.allowFreeText,
        multiple: prompt.multiple,
        prompt: prompt.prompt,
        title: prompt.title,
        id: prompt.id,
      })),
    };
    assert.equal(sameQuestionRequest(question, reordered), true);
    assert.equal(
      sameQuestionRequest(question, {
        ...reordered,
        questions: reordered.questions.map((prompt) => ({
          ...prompt,
          options: prompt.options?.toReversed(),
        })),
      }),
      false,
    );
  });
  it("binds reads and answer receipts to exact scope and private cache semantics", async () => {
    const question = pendingQuestion();
    const calls: unknown[] = [];
    const client = {
      GET: async (...args: unknown[]) => {
        calls.push(args);
        return { data: question, response: new Response(null, { status: 200 }) };
      },
      POST: async (...args: unknown[]) => {
        calls.push(args);
        return { data: answeredQuestion(question), response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;
    assert.equal((await readInvocationQuestion(client, reference)).ok, true);
    const answers = [{ questionId: "item_1", optionIds: ["option_2"] }];
    assert.equal(
      (await answerInvocationQuestion(client, reference, question, answers, "question:fixed001"))
        .ok,
      true,
    );
    const path =
      "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/questions/{questionId}";
    assert.deepEqual(calls, [
      [path, { params: { path: reference }, cache: "no-store" }],
      [
        `${path}/answers`,
        {
          body: { expectedVersion: 1, answers },
          params: { path: reference, header: { "Idempotency-Key": "question:fixed001" } },
          cache: "no-store",
        },
      ],
    ]);
  });
  it("rejects leaked fields, substituted scope, inconsistent states and unbounded prompts", () => {
    const question = pendingQuestion();
    assert.equal(matchesQuestion(question, reference), true);
    for (const patch of [
      { answers: [] },
      { operationId: "native" },
      { id: "native-question" },
      { isolationDomainId: "iso_00000000000000000002" },
      { serviceId: "native" },
      { state: "future" },
      { state: "answered" },
      { version: 2 },
      { answeredBy: "someone" },
      { closeReason: "expired" },
      { expiresAt: question.createdAt },
      { expiresAt: new Date(Date.parse(question.createdAt) + 900001).toISOString() },
      { questions: [] },
      { questions: [{ ...question.questions[0], title: "🔒".repeat(33) }] },
      { questions: [{ ...question.questions[0], prompt: "\ud800" }] },
    ])
      assert.equal(
        matchesQuestion({ ...question, ...patch }, reference),
        false,
        JSON.stringify(patch),
      );
    const answered = answeredQuestion(question);
    assert.equal(matchesQuestion(answered, reference), true);
    assert.equal(
      matchesQuestion({ ...answered, state: "delivering", version: 3 }, reference),
      true,
    );
    assert.equal(matchesQuestion({ ...answered, state: "delivered", version: 4 }, reference), true);
    assert.equal(
      matchesQuestion(
        {
          ...answered,
          state: "delivery_unknown",
          version: 4,
          closedAt: question.expiresAt,
          updatedAt: question.expiresAt,
          closeReason: "expired",
        },
        reference,
      ),
      true,
    );
  });
  it("validates every answer against frozen choices, exclusivity and UTF-8 limits", () => {
    const prompts = pendingQuestion().questions;
    for (const answers of [
      [],
      [{ questionId: "other", text: "yes" }],
      [{ questionId: "item_1", optionIds: ["option_3"] }],
      [{ questionId: "item_1", optionIds: ["option_1", "option_2"] }],
      [{ questionId: "item_1", optionIds: ["option_1"], text: "mixed" }],
      [{ questionId: "item_1", text: " " }],
      [{ questionId: "item_1", text: "😀".repeat(1025) }],
      [{ questionId: "item_1", text: "\r" }],
      [{ questionId: "item_1", text: "answer", nativeId: "hidden" }],
    ])
      assert.equal(validQuestionAnswers(prompts, answers), false);
    assert.equal(
      validQuestionAnswers(prompts, [{ questionId: "item_1", text: "😀".repeat(1024) }]),
      true,
    );
    const multiple = prompts.map((prompt) => ({ ...prompt, multiple: true, allowFreeText: false }));
    assert.equal(
      validQuestionAnswers(multiple, [
        { questionId: "item_1", optionIds: ["option_1", "option_2"] },
      ]),
      true,
    );
    assert.equal(
      validQuestionAnswers(multiple, [
        { questionId: "item_1", optionIds: ["option_1", "option_1"] },
      ]),
      false,
    );
    assert.equal(
      validQuestionAnswers(multiple, [{ questionId: "item_1", text: "unoffered" }]),
      false,
    );
    assert.equal(validQuestionPrompts([{ ...prompts[0], options: [] }]), false);
    assert.equal(
      validQuestionPrompts([
        { ...prompts[0], options: undefined, multiple: false, allowFreeText: true },
      ]),
      true,
    );
  });
  it("rejects invalid requests before transport and never surfaces upstream error content", async () => {
    let calls = 0;
    const client = {
      POST: async () => {
        calls++;
        throw new Error("private answer leaked upstream");
      },
      GET: async () => ({
        error: {
          error: {
            code: "INVOCATION_QUESTION_FORBIDDEN",
            message: "private answer leaked upstream",
            correlationId: "cor_00000000000000000001",
          },
        },
        response: new Response(null, { status: 403 }),
      }),
    } as unknown as DataGroundClient;
    const question = pendingQuestion();
    assert.equal(
      (await answerInvocationQuestion(client, reference, question, [], "question:fixed001")).ok,
      false,
    );
    assert.equal(calls, 0);
    for (const result of [
      await readInvocationQuestion(client, reference),
      await answerInvocationQuestion(
        client,
        reference,
        question,
        [{ questionId: "item_1", text: "hello" }],
        "question:fixed001",
      ),
    ]) {
      assert.equal(result.ok, false);
      assert.doesNotMatch(JSON.stringify(result), /private answer/u);
    }
  });
  it("requires exact success status and an unchanged question in an accepted receipt", async () => {
    const question = pendingQuestion();
    for (const [data, status] of [
      [question, 200],
      [answeredQuestion(question), 202],
      [
        {
          ...answeredQuestion(question),
          questions: [{ ...question.questions[0], prompt: "substitution" }],
        },
        200,
      ],
    ] as const) {
      const client = {
        POST: async () => ({ data, response: new Response(null, { status }) }),
      } as unknown as DataGroundClient;
      assert.equal(
        (
          await answerInvocationQuestion(
            client,
            reference,
            question,
            [{ questionId: "item_1", text: "hello" }],
            "question:fixed001",
          )
        ).ok,
        false,
      );
    }
  });
});
