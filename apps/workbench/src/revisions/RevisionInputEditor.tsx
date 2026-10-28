import { Button, TextField } from "@dataground/ui";
import { useRef, useState } from "react";
import { createPromptInputSchema, readPromptInputSchema } from "./promptInput";

interface RevisionInputEditorProps {
  value: string;
  error?: string;
  disabled: boolean;
  onChange: (value: string) => void;
}

export function RevisionInputEditor({
  value,
  error,
  disabled,
  onChange,
}: RevisionInputEditorProps) {
  const [jsonMode, setJsonMode] = useState(false);
  const root = useRef<HTMLFieldSetElement>(null);
  const prompt = readPromptInputSchema(value);
  const guided = prompt !== undefined && !jsonMode;
  const moveFocus = () => root.current?.focus();
  const usePromptInput = () => {
    if (disabled || value.trim() !== "") return;
    moveFocus();
    setJsonMode(false);
    onChange(
      createPromptInputSchema({
        label: "Prompt",
        guidance: "Describe what you want the service to do.",
      }),
    );
  };
  return (
    <fieldset className="revision-input-editor" ref={root} tabIndex={-1} disabled={disabled}>
      <legend>Interaction input</legend>
      <p>Choose the information people must provide when they run this service.</p>
      {value.trim() === "" && !jsonMode && error && <p role="alert">{error}</p>}
      {value.trim() === "" && !jsonMode ? (
        <div className="revision-input-editor__choice">
          <h3>Start with a text prompt</h3>
          <p>Add one required text field named prompt, with a limit of 262,144 characters.</p>
          <Button isDisabled={disabled} onPress={usePromptInput}>
            Use prompt input
          </Button>
          <Button
            isDisabled={disabled}
            variant="quiet"
            onPress={() => {
              if (disabled) return;
              moveFocus();
              setJsonMode(true);
            }}
          >
            Write custom JSON
          </Button>
          <p>
            No input contract has been selected. The interaction form requires a supported input
            contract.
          </p>
        </div>
      ) : guided ? (
        <>
          <p>
            Required text field: <code>prompt</code>. Length: 1 to 262,144 characters.
          </p>
          <TextField
            label="Prompt label (optional)"
            description="The field name shown in the interaction form. Defaults to prompt."
            maxLength={128}
            isDisabled={disabled}
            value={prompt.label}
            onChange={(label) => {
              if (!disabled) onChange(createPromptInputSchema({ ...prompt, label }));
            }}
          />
          <TextField
            label="Prompt guidance (optional)"
            description="Help people describe the task. This is form guidance, not an instruction to the agent."
            maxLength={512}
            isMultiline
            isDisabled={disabled}
            value={prompt.guidance}
            onChange={(guidance) => {
              if (!disabled) onChange(createPromptInputSchema({ ...prompt, guidance }));
            }}
          />
          <Button
            isDisabled={disabled}
            variant="quiet"
            onPress={() => {
              if (disabled) return;
              moveFocus();
              setJsonMode(true);
            }}
          >
            Edit JSON
          </Button>
          {error && <p role="alert">{error}</p>}
        </>
      ) : (
        <>
          <TextField
            label="Input schema"
            description="JSON Schema for accepted invocation input. Custom definitions are preserved exactly."
            isMultiline
            isDisabled={disabled}
            errorMessage={error}
            value={value}
            onChange={(next) => {
              if (!disabled) onChange(next);
            }}
          />
          {prompt && (
            <Button
              isDisabled={disabled}
              variant="quiet"
              onPress={() => {
                if (disabled) return;
                moveFocus();
                setJsonMode(false);
              }}
            >
              Use guided editor
            </Button>
          )}
          {!prompt && (
            <p>
              Guided editing is available only for the exact prompt template. Custom schemas stay in
              JSON mode.
            </p>
          )}
          {value.trim() === "" && (
            <Button isDisabled={disabled} onPress={usePromptInput}>
              Use prompt input
            </Button>
          )}
        </>
      )}
    </fieldset>
  );
}
