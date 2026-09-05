import { Button, StatusBadge, type StatusTone, TextField } from "@dataground/ui";
import { useId } from "react";

export interface QuestionItem {
  id: string;
  title: string;
  prompt: string;
  options?: { id: string; label: string; description: string }[];
  multiple: boolean;
  allowFreeText: boolean;
}
export interface QuestionDraft {
  optionIds: string[];
  text: string;
  useFreeText: boolean;
}
export interface QuestionRequestProps {
  question: {
    id: string;
    invocationId: string;
    isolationDomainId: string;
    questions: QuestionItem[];
    state: string;
    expiresAt: string;
    answeredBy?: string;
  };
  drafts: Record<string, QuestionDraft>;
  canAnswer: boolean;
  complete: boolean;
  expired: boolean;
  frozen?: boolean;
  busy?: boolean;
  disabledReason?: string;
  error?: { message: string; correlationId?: string };
  onDraft: (id: string, draft: QuestionDraft) => void;
  onSubmit: () => void;
  onRefresh: () => void;
}
const states: Record<string, { label: string; tone: StatusTone }> = {
  pending: { label: "Waiting for answers", tone: "waiting" },
  answered: { label: "Answers recorded", tone: "active" },
  delivering: { label: "Delivering answers", tone: "active" },
  delivered: { label: "Answers delivered", tone: "success" },
  expired: { label: "Question expired", tone: "warning" },
  closed: { label: "Question closed", tone: "neutral" },
  delivery_unknown: { label: "Answer delivery unknown", tone: "warning" },
};
export function QuestionRequest({
  question,
  drafts,
  canAnswer,
  complete,
  expired,
  frozen = false,
  busy = false,
  disabledReason,
  error,
  onDraft,
  onSubmit,
  onRefresh,
}: QuestionRequestProps) {
  const titleId = useId();
  const presentation = states[
    expired && question.state === "pending" ? "expired" : question.state
  ] ?? { label: "Unknown question state", tone: "neutral" };
  const pending = question.state === "pending" && !expired;
  const editable = pending && canAnswer && !busy && !frozen && !disabledReason;
  return (
    <section
      aria-labelledby={titleId}
      aria-busy={busy || undefined}
      className="dg-question-request"
    >
      <div className="dg-question-request__heading">
        <h2 id={titleId}>Invocation questions</h2>
        <span aria-live="polite">
          <StatusBadge tone={presentation.tone}>{presentation.label}</StatusBadge>
        </span>
      </div>
      <dl className="dg-question-request__facts">
        <div>
          <dt>Isolation domain</dt>
          <dd>{question.isolationDomainId}</dd>
        </div>
        <div>
          <dt>Invocation</dt>
          <dd>{question.invocationId}</dd>
        </div>
        <div>
          <dt>Question</dt>
          <dd>{question.id}</dd>
        </div>
        <div>
          <dt>Answer by</dt>
          <dd>
            <time dateTime={question.expiresAt}>{question.expiresAt}</time>
          </dd>
        </div>
        {question.answeredBy && (
          <div>
            <dt>Answered by</dt>
            <dd>{question.answeredBy}</dd>
          </div>
        )}
      </dl>
      <p>
        These questions come from the runtime. Answers provide input; they do not grant permission
        to run tools or change the workspace.
      </p>
      {question.questions.map((item) => {
        const draft = drafts[item.id] ?? {
          optionIds: [],
          text: "",
          useFreeText: !item.options?.length,
        };
        const promptId = `${titleId}-${item.id}-prompt`;
        return (
          <fieldset key={item.id} className="dg-question-request__item" aria-describedby={promptId}>
            <legend>{item.title}</legend>
            <p id={promptId}>{item.prompt}</p>
            {pending && canAnswer ? (
              <>
                {item.options && (
                  <>
                    <p>{item.multiple ? "Choose one or more options." : "Choose one option."}</p>
                    {item.options.map((option) => (
                      <label key={option.id} className="dg-question-request__choice">
                        <input
                          type={item.multiple ? "checkbox" : "radio"}
                          name={`${titleId}-${item.id}`}
                          checked={!draft.useFreeText && draft.optionIds.includes(option.id)}
                          disabled={!editable || draft.useFreeText}
                          onChange={(event) =>
                            onDraft(item.id, {
                              ...draft,
                              useFreeText: false,
                              text: "",
                              optionIds: item.multiple
                                ? event.target.checked
                                  ? [...draft.optionIds, option.id]
                                  : draft.optionIds.filter((id) => id !== option.id)
                                : [option.id],
                            })
                          }
                        />
                        <span>
                          {option.label}
                          {option.description && <small>{option.description}</small>}
                        </span>
                      </label>
                    ))}
                    {item.allowFreeText && (
                      <label className="dg-question-request__choice">
                        <input
                          type="checkbox"
                          checked={draft.useFreeText}
                          disabled={!editable}
                          onChange={(event) =>
                            onDraft(item.id, {
                              optionIds: [],
                              text: "",
                              useFreeText: event.target.checked,
                            })
                          }
                        />
                        <span>Write my own answer for {item.title}</span>
                      </label>
                    )}
                  </>
                )}
                {draft.useFreeText && item.allowFreeText && (
                  <TextField
                    isMultiline
                    isDisabled={!editable}
                    label={`Answer for ${item.title}`}
                    description={`${new TextEncoder().encode(draft.text).length} of 4096 UTF-8 bytes. Avoid credentials or other secrets.`}
                    errorMessage={
                      new TextEncoder().encode(draft.text).length > 4096
                        ? "Shorten this answer to 4096 UTF-8 bytes or fewer."
                        : undefined
                    }
                    value={draft.text}
                    maxLength={4096}
                    onChange={(text) => onDraft(item.id, { ...draft, text })}
                  />
                )}
              </>
            ) : (
              item.options && (
                <ul>
                  {item.options.map((option) => (
                    <li key={option.id}>
                      {option.label}
                      {option.description && <span> — {option.description}</span>}
                    </li>
                  ))}
                </ul>
              )
            )}
          </fieldset>
        );
      })}
      {pending && !canAnswer && <p>This view can observe questions but cannot submit answers.</p>}
      {expired && question.state === "pending" && (
        <p>The answer deadline has passed. Refresh to confirm the recorded state.</p>
      )}
      {question.state === "answered" && (
        <p>DataGround recorded the answers. Delivery to the runtime has not been confirmed.</p>
      )}
      {question.state === "delivery_unknown" && (
        <p>The runtime may have received the answers. DataGround will not repeat delivery.</p>
      )}
      {frozen && pending && (
        <p>
          The submitted answers are retained for an exact retry. Refresh the state before retrying.
        </p>
      )}
      {disabledReason && <p>{disabledReason}</p>}
      {error && (
        <div role="alert" className="dg-question-request__error">
          <p>{error.message}</p>
          {error.correlationId && (
            <p>
              Correlation: <code>{error.correlationId}</code>
            </p>
          )}
        </div>
      )}
      <div className="dg-question-request__actions">
        {pending && canAnswer && (
          <Button
            isPending={busy}
            isDisabled={!complete || Boolean(disabledReason)}
            onPress={onSubmit}
            variant="primary"
          >
            {frozen ? "Retry same answers" : "Submit answers"}
          </Button>
        )}
        <Button isDisabled={busy} onPress={onRefresh} variant="quiet">
          Refresh question
        </Button>
      </div>
    </section>
  );
}
