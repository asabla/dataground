import type { InvocationQuestion } from "./client";

export const questionReference = {
  isolationDomainId: "iso_00000000000000000001",
  invocationId: "inv_00000000000000000001",
  questionId: "qst_00000000000000000001",
};
export function pendingQuestion(now = Date.now()): InvocationQuestion {
  return {
    schemaVersion: "dataground.invocation-question/v1",
    id: questionReference.questionId,
    isolationDomainId: questionReference.isolationDomainId,
    invocationId: questionReference.invocationId,
    serviceId: "svc_00000000000000000001",
    revisionId: "rev_00000000000000000001",
    state: "pending",
    version: 1,
    createdAt: new Date(now - 1000).toISOString(),
    updatedAt: new Date(now - 1000).toISOString(),
    expiresAt: new Date(now + 60000).toISOString(),
    questions: [
      {
        id: "item_1",
        title: "Target",
        prompt: "Which environment should the report describe?",
        multiple: false,
        allowFreeText: true,
        options: [
          { id: "option_1", label: "Development", description: "The local workspace." },
          { id: "option_2", label: "Staging", description: "The staging environment." },
        ],
      },
    ],
  };
}
export function answeredQuestion(question: InvocationQuestion): InvocationQuestion {
  return {
    ...question,
    state: "answered",
    version: 2,
    answeredBy: "usr_00000000000000000001",
    answeredAt: new Date(Date.parse(question.createdAt) + 500).toISOString(),
    updatedAt: new Date(Date.parse(question.createdAt) + 500).toISOString(),
  };
}
