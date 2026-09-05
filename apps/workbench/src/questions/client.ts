import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";

export type InvocationQuestion = components["schemas"]["InvocationQuestion"];
export type QuestionAnswer = components["schemas"]["QuestionAnswer"];
export type QuestionPrompt = components["schemas"]["QuestionPrompt"];
export interface InvocationQuestionReference {
  isolationDomainId: string;
  invocationId: string;
  questionId: string;
}
export interface QuestionFailure {
  code: string;
  message: string;
  correlationId?: string;
  status?: number;
  retryable: boolean;
}
export type QuestionResult =
  | { ok: true; question: InvocationQuestion }
  | { ok: false; error: QuestionFailure };

const questionPath =
  "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/questions/{questionId}";
const itemID = /^[a-z][a-z0-9_]{0,63}$/u;
const timestamp = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/u;
function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function keys(value: Record<string, unknown>, allowed: string[]) {
  return Object.keys(value).every((key) => allowed.includes(key));
}
function id(value: unknown, prefix: string) {
  return typeof value === "string" && new RegExp(`^${prefix}_[0-9a-z]{20,32}$`, "u").test(value);
}
function time(value: unknown): value is string {
  return typeof value === "string" && timestamp.test(value) && Number.isFinite(Date.parse(value));
}
function text(value: unknown, maximum: number, empty = false): value is string {
  return (
    typeof value === "string" &&
    value.isWellFormed() &&
    (empty || value.trim().length > 0) &&
    new TextEncoder().encode(value).length <= maximum &&
    !Array.from(value).some(
      (character) => /\p{Cc}/u.test(character) && character !== "\n" && character !== "\t",
    )
  );
}
// Match the server's JSON byte budget, including Go's HTML-safe escapes.
function jsonBytes(value: unknown) {
  return new TextEncoder().encode(
    JSON.stringify(value).replace(
      /[<>&\u2028\u2029]/gu,
      (character) => `\\u${character.charCodeAt(0).toString(16).padStart(4, "0")}`,
    ),
  ).length;
}
export function validQuestionReference(reference: InvocationQuestionReference) {
  return (
    id(reference.isolationDomainId, "iso") &&
    id(reference.invocationId, "inv") &&
    id(reference.questionId, "qst")
  );
}
export function validQuestionPrompts(value: unknown): value is QuestionPrompt[] {
  if (!Array.isArray(value) || value.length < 1 || value.length > 3) return false;
  const seen = new Set<string>();
  for (const prompt of value) {
    if (
      !record(prompt) ||
      !keys(prompt, ["id", "title", "prompt", "options", "multiple", "allowFreeText"]) ||
      typeof prompt.id !== "string" ||
      !itemID.test(prompt.id) ||
      seen.has(prompt.id) ||
      !text(prompt.title, 128) ||
      !text(prompt.prompt, 2048) ||
      typeof prompt.multiple !== "boolean" ||
      typeof prompt.allowFreeText !== "boolean"
    )
      return false;
    seen.add(prompt.id);
    if (prompt.options === undefined) {
      if (!prompt.allowFreeText || prompt.multiple) return false;
      continue;
    }
    if (!Array.isArray(prompt.options) || prompt.options.length < 2 || prompt.options.length > 4)
      return false;
    const ids = new Set<string>();
    const labels = new Set<string>();
    for (const option of prompt.options) {
      if (
        !record(option) ||
        !keys(option, ["id", "label", "description"]) ||
        typeof option.id !== "string" ||
        !itemID.test(option.id) ||
        ids.has(option.id) ||
        !text(option.label, 256) ||
        labels.has(option.label.trim()) ||
        !text(option.description, 1024, true)
      )
        return false;
      ids.add(option.id);
      labels.add(option.label.trim());
    }
  }
  return jsonBytes(value) <= 32768;
}
export function validQuestionAnswers(
  prompts: QuestionPrompt[],
  answers: unknown,
): answers is QuestionAnswer[] {
  if (
    !validQuestionPrompts(prompts) ||
    !Array.isArray(answers) ||
    answers.length !== prompts.length
  )
    return false;
  const seen = new Set<string>();
  for (const answer of answers) {
    if (
      !record(answer) ||
      !keys(answer, ["questionId", "optionIds", "text"]) ||
      typeof answer.questionId !== "string" ||
      seen.has(answer.questionId)
    )
      return false;
    const prompt = prompts.find((item) => item.id === answer.questionId);
    if (!prompt) return false;
    seen.add(answer.questionId);
    if (answer.text !== undefined) {
      if (!prompt.allowFreeText || answer.optionIds !== undefined || !text(answer.text, 4096))
        return false;
    } else {
      if (
        !Array.isArray(answer.optionIds) ||
        answer.optionIds.length < 1 ||
        answer.optionIds.length > (prompt.multiple ? 4 : 1) ||
        new Set(answer.optionIds).size !== answer.optionIds.length ||
        !answer.optionIds.every((id) => prompt.options?.some((option) => option.id === id))
      )
        return false;
    }
  }
  return jsonBytes(answers) <= 16384;
}
export function matchesQuestion(
  value: unknown,
  reference: InvocationQuestionReference,
): value is InvocationQuestion {
  if (
    !record(value) ||
    !keys(value, [
      "schemaVersion",
      "id",
      "isolationDomainId",
      "invocationId",
      "serviceId",
      "revisionId",
      "questions",
      "state",
      "version",
      "expiresAt",
      "answeredBy",
      "answeredAt",
      "closedAt",
      "closeReason",
      "createdAt",
      "updatedAt",
    ]) ||
    value.schemaVersion !== "dataground.invocation-question/v1" ||
    value.id !== reference.questionId ||
    value.isolationDomainId !== reference.isolationDomainId ||
    value.invocationId !== reference.invocationId ||
    !id(value.serviceId, "svc") ||
    !id(value.revisionId, "rev") ||
    !validQuestionPrompts(value.questions) ||
    !time(value.createdAt) ||
    !time(value.updatedAt) ||
    !time(value.expiresAt) ||
    Date.parse(value.updatedAt) < Date.parse(value.createdAt) ||
    Date.parse(value.expiresAt) <= Date.parse(value.createdAt) ||
    Date.parse(value.expiresAt) - Date.parse(value.createdAt) > 900000
  )
    return false;
  const versions: Record<string, number[]> = {
    pending: [1],
    answered: [2],
    delivering: [3],
    delivered: [4],
    closed: [2, 3],
    expired: [2, 3],
    delivery_unknown: [4],
  };
  if (
    typeof value.state !== "string" ||
    !Object.hasOwn(versions, value.state) ||
    !versions[value.state]?.includes(value.version as number)
  )
    return false;
  const answered = value.answeredBy !== undefined || value.answeredAt !== undefined;
  if (
    answered &&
    (!text(value.answeredBy, 256) ||
      !time(value.answeredAt) ||
      Date.parse(value.answeredAt) < Date.parse(value.createdAt) ||
      Date.parse(value.answeredAt) >= Date.parse(value.expiresAt) ||
      Date.parse(value.answeredAt) > Date.parse(value.updatedAt))
  )
    return false;
  if (
    ["answered", "delivering", "delivered", "delivery_unknown"].includes(value.state) !==
      answered &&
    !["closed", "expired"].includes(value.state)
  )
    return false;
  const closed = ["closed", "expired", "delivery_unknown"].includes(value.state);
  if (closed) {
    if (
      !time(value.closedAt) ||
      Date.parse(value.closedAt) < Date.parse(value.createdAt) ||
      Date.parse(value.closedAt) > Date.parse(value.updatedAt) ||
      ![
        "expired",
        "runtime-request-cleared",
        "cancelled",
        "runtime-ended",
        "delivery-ambiguous",
      ].includes(String(value.closeReason))
    )
      return false;
    if (
      value.state === "expired" &&
      (value.closeReason !== "expired" || Date.parse(value.closedAt) < Date.parse(value.expiresAt))
    )
      return false;
    if (value.state !== "delivery_unknown" && value.version !== (answered ? 3 : 2)) return false;
  } else if (value.closedAt !== undefined || value.closeReason !== undefined) return false;
  return true;
}
function failure(
  code: string,
  message: string,
  retryable = false,
  status?: number,
): QuestionResult {
  return { ok: false, error: { code, message, retryable, status } };
}
function failed(error: unknown, status: number): QuestionResult {
  const problem = record(error) && record(error.error) ? error.error : undefined;
  const messages: Record<number, string> = {
    400: "The answer was rejected. Refresh the question before retrying.",
    401: "Sign in again to access this question.",
    403: "You are not authorized to answer this question.",
    404: "The question was not found.",
    409: "The question changed or was already answered. Refresh its state.",
    410: "The question has expired.",
  };
  return {
    ok: false,
    error: {
      code:
        typeof problem?.code === "string" && /^[A-Z][A-Z0-9_]{0,127}$/u.test(problem.code)
          ? problem.code
          : "WORKBENCH_INVALID_RESPONSE",
      correlationId: id(problem?.correlationId, "cor") ? String(problem?.correlationId) : undefined,
      message:
        messages[status] ??
        "DataGround could not confirm the question state. Refresh before retrying.",
      retryable: status >= 500,
      status,
    },
  };
}
export async function readInvocationQuestion(
  client: DataGroundClient,
  reference: InvocationQuestionReference,
): Promise<QuestionResult> {
  if (!validQuestionReference(reference))
    return failure("WORKBENCH_INVALID_REQUEST", "The question reference is invalid.");
  try {
    const { data, error, response } = await client.GET(questionPath, {
      params: { path: reference },
      cache: "no-store",
    });
    if (response.status !== 200 || !data) return failed(error, response.status);
    return matchesQuestion(data, reference)
      ? { ok: true, question: data }
      : failure(
          "WORKBENCH_INVALID_RESPONSE",
          "DataGround returned question data outside the expected scope or contract.",
        );
  } catch {
    return failure(
      "WORKBENCH_NETWORK_UNAVAILABLE",
      "The Workbench could not reach DataGround.",
      true,
    );
  }
}
export async function answerInvocationQuestion(
  client: DataGroundClient,
  reference: InvocationQuestionReference,
  question: InvocationQuestion,
  answers: QuestionAnswer[],
  idempotencyKey: string,
): Promise<QuestionResult> {
  if (
    !validQuestionReference(reference) ||
    !matchesQuestion(question, reference) ||
    question.state !== "pending" ||
    !validQuestionAnswers(question.questions, answers) ||
    !/^[A-Za-z0-9._:-]{8,128}$/u.test(idempotencyKey)
  )
    return failure(
      "WORKBENCH_INVALID_REQUEST",
      "Complete every question using the offered choices or permitted free text.",
    );
  try {
    const { data, error, response } = await client.POST(`${questionPath}/answers`, {
      body: { expectedVersion: 1, answers },
      params: { path: reference, header: { "Idempotency-Key": idempotencyKey } },
      cache: "no-store",
    });
    if (response.status !== 200 || !data) return failed(error, response.status);
    return matchesQuestion(data, reference) &&
      data.answeredBy !== undefined &&
      sameQuestionRequest(question, data)
      ? { ok: true, question: data }
      : failure(
          "WORKBENCH_INVALID_RESPONSE",
          "The answer outcome could not be confirmed. Refresh the question before retrying.",
          true,
        );
  } catch {
    return failure(
      "WORKBENCH_NETWORK_UNAVAILABLE",
      "The answer outcome is unknown. Refresh the question before retrying.",
      true,
    );
  }
}
export function sameQuestionRequest(left: InvocationQuestion, right: InvocationQuestion) {
  return (
    left.id === right.id &&
    left.isolationDomainId === right.isolationDomainId &&
    left.invocationId === right.invocationId &&
    left.serviceId === right.serviceId &&
    left.revisionId === right.revisionId &&
    left.createdAt === right.createdAt &&
    left.expiresAt === right.expiresAt &&
    left.questions.length === right.questions.length &&
    left.questions.every((prompt, index) => {
      const other = right.questions[index];
      return (
        other &&
        prompt.id === other.id &&
        prompt.title === other.title &&
        prompt.prompt === other.prompt &&
        prompt.multiple === other.multiple &&
        prompt.allowFreeText === other.allowFreeText &&
        (prompt.options?.length ?? 0) === (other.options?.length ?? 0) &&
        (prompt.options ?? []).every((option, index) => {
          const candidate = other.options?.[index];
          return (
            candidate &&
            option.id === candidate.id &&
            option.label === candidate.label &&
            option.description === candidate.description
          );
        })
      );
    })
  );
}
