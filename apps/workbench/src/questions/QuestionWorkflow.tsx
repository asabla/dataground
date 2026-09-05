import { type QuestionDraft, QuestionRequest } from "@dataground/patterns";
import "@dataground/patterns/styles.css";
import { Button, StatusBadge } from "@dataground/ui";
import { useCallback, useEffect, useRef, useState } from "react";
import type { DataGroundClient } from "../contracts/client";
import {
  answerInvocationQuestion,
  type InvocationQuestion,
  type InvocationQuestionReference,
  type QuestionAnswer,
  type QuestionFailure,
  readInvocationQuestion,
  sameQuestionRequest,
  validQuestionAnswers,
} from "./client";

export interface QuestionWorkflowProps {
  client: DataGroundClient;
  reference: InvocationQuestionReference;
  canAnswer: boolean;
  createIdempotencyKey?: () => string;
}
interface AnswerAttempt {
  answers: QuestionAnswer[];
  idempotencyKey: string;
}
interface State {
  question?: InvocationQuestion;
  drafts: Record<string, QuestionDraft>;
  attempt?: AnswerAttempt;
  error?: QuestionFailure;
  loading: boolean;
  submitting: boolean;
  expired: boolean;
}
function defaultKey() {
  return `question:${globalThis.crypto.randomUUID().replaceAll("-", "")}`;
}
export function draftQuestionAnswers(
  question: InvocationQuestion,
  drafts: Record<string, QuestionDraft>,
): QuestionAnswer[] {
  return question.questions.map((item) => {
    const draft = drafts[item.id];
    return draft?.useFreeText
      ? { questionId: item.id, text: draft.text }
      : { questionId: item.id, optionIds: draft?.optionIds ?? [] };
  });
}
export function QuestionWorkflow(props: QuestionWorkflowProps) {
  const key = `${props.reference.isolationDomainId}:${props.reference.invocationId}:${props.reference.questionId}:${props.canAnswer}`;
  const [identity, setIdentity] = useState({ client: props.client, key, generation: 0 });
  if (identity.client !== props.client || identity.key !== key) {
    setIdentity({ client: props.client, key, generation: identity.generation + 1 });
    return null;
  }
  // Remount before exposing content whenever identity, scope or authority changes.
  // Drafts and pending completions belong only to this exact inspection session.
  return <QuestionSession key={identity.generation} {...props} />;
}
function QuestionSession({
  client,
  reference,
  canAnswer,
  createIdempotencyKey = defaultKey,
}: QuestionWorkflowProps) {
  const [state, setState] = useState<State>({
    drafts: {},
    loading: true,
    submitting: false,
    expired: false,
  });
  const generation = useRef(0);
  const lock = useRef(false);
  const [scope] = useState(reference);
  const refresh = useCallback(async () => {
    if (lock.current) return;
    lock.current = true;
    const current = ++generation.current;
    setState((state) => ({ ...state, loading: true }));
    const result = await readInvocationQuestion(client, scope);
    if (generation.current !== current) return;
    lock.current = false;
    setState((state) => {
      if (!result.ok)
        return {
          ...state,
          loading: false,
          error: result.error,
          ...([401, 403].includes(result.error.status ?? 0)
            ? { question: undefined, drafts: {}, attempt: undefined }
            : {}),
        };
      if (
        state.question &&
        (!sameQuestionRequest(state.question, result.question) ||
          result.question.version < state.question.version)
      ) {
        return {
          ...state,
          loading: false,
          error: {
            code: "WORKBENCH_INVALID_RESPONSE",
            message:
              "The question changed unexpectedly. Close this inspection and reopen it from the timeline.",
            retryable: false,
          },
        };
      }
      const pending = result.question.state === "pending";
      return {
        ...state,
        question: result.question,
        loading: false,
        error: undefined,
        expired: state.expired || Date.now() >= Date.parse(result.question.expiresAt),
        drafts: pending ? state.drafts : {},
        attempt: pending ? state.attempt : undefined,
      };
    });
  }, [client, scope]);
  useEffect(() => {
    void refresh();
    return () => {
      generation.current++;
      lock.current = false;
    };
  }, [refresh]);
  useEffect(() => {
    if (!state.question || state.expired) return;
    const expires = Date.parse(state.question.expiresAt);
    const check = () => {
      if (Date.now() >= expires)
        setState((state) => ({ ...state, expired: true, drafts: {}, attempt: undefined }));
    };
    const timer = setInterval(check, 250);
    check();
    return () => clearInterval(timer);
  }, [state.question, state.expired]);
  async function submit() {
    const question = state.question;
    if (
      !canAnswer ||
      lock.current ||
      !question ||
      question.state !== "pending" ||
      state.error ||
      state.expired ||
      Date.now() >= Date.parse(question.expiresAt)
    )
      return;
    const answers = state.attempt?.answers ?? draftQuestionAnswers(question, state.drafts);
    if (!validQuestionAnswers(question.questions, answers)) return;
    let attempt: AnswerAttempt;
    try {
      attempt = state.attempt ?? {
        answers: structuredClone(answers),
        idempotencyKey: createIdempotencyKey(),
      };
    } catch {
      setState((state) => ({
        ...state,
        error: {
          code: "WORKBENCH_SECURE_RANDOM_UNAVAILABLE",
          message: "A secure request identifier could not be created. Refresh before retrying.",
          retryable: false,
        },
      }));
      return;
    }
    lock.current = true;
    const current = ++generation.current;
    setState((state) => ({ ...state, attempt, submitting: true }));
    const result = await answerInvocationQuestion(
      client,
      scope,
      question,
      attempt.answers,
      attempt.idempotencyKey,
    );
    if (generation.current !== current) return;
    lock.current = false;
    setState((state) =>
      result.ok
        ? {
            ...state,
            question: result.question,
            drafts: {},
            attempt: undefined,
            submitting: false,
            error: undefined,
          }
        : { ...state, submitting: false, error: result.error },
    );
  }
  if (!state.question)
    return (
      <section aria-live="polite" className="question-workflow-state">
        <StatusBadge tone={state.loading ? "active" : "critical"}>
          {state.loading ? "Loading question" : "Question unavailable"}
        </StatusBadge>
        {state.error && <p>{state.error.message}</p>}
        {!state.loading && (
          <Button onPress={() => void refresh()} variant="secondary">
            Retry question read
          </Button>
        )}
      </section>
    );
  const answers = state.attempt?.answers ?? draftQuestionAnswers(state.question, state.drafts);
  return (
    <QuestionRequest
      question={state.question}
      drafts={state.drafts}
      canAnswer={canAnswer}
      complete={validQuestionAnswers(state.question.questions, answers)}
      expired={state.expired}
      busy={state.loading || state.submitting}
      frozen={Boolean(state.attempt)}
      error={state.error}
      disabledReason={
        state.error
          ? "Refresh the authoritative question state before submitting."
          : state.loading
            ? "Refreshing the question state."
            : undefined
      }
      onDraft={(id, draft) => {
        if (!lock.current && !state.attempt && !state.expired && canAnswer)
          setState((state) => ({ ...state, drafts: { ...state.drafts, [id]: draft } }));
      }}
      onSubmit={() => void submit()}
      onRefresh={() => void refresh()}
    />
  );
}
