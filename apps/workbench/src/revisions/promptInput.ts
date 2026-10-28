export interface PromptInputDefinition {
  label: string;
  guidance: string;
}

export function createPromptInputSchema({ label, guidance }: PromptInputDefinition): string {
  return JSON.stringify(
    {
      type: "object",
      additionalProperties: false,
      properties: {
        prompt: {
          type: "string",
          minLength: 1,
          maxLength: 262144,
          ...(label.trim() ? { title: label } : {}),
          ...(guidance.trim() ? { description: guidance } : {}),
        },
      },
      required: ["prompt"],
    },
    null,
    2,
  );
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function onlyKeys(value: Record<string, unknown>, keys: readonly string[]) {
  return Object.keys(value).every((key) => keys.includes(key));
}

// Recognize only this exact contract. Extra validation or annotations remain in
// JSON mode; a guided edit must never silently remove a user's schema semantics.
export function readPromptInputSchema(value: string): PromptInputDefinition | undefined {
  if (value.length > 1 << 20) return undefined;
  let schema: unknown;
  try {
    schema = JSON.parse(value);
  } catch {
    return undefined;
  }
  if (
    !record(schema) ||
    !onlyKeys(schema, ["type", "additionalProperties", "properties", "required"]) ||
    schema.type !== "object" ||
    schema.additionalProperties !== false ||
    !Array.isArray(schema.required) ||
    schema.required.length !== 1 ||
    schema.required[0] !== "prompt" ||
    !record(schema.properties) ||
    !onlyKeys(schema.properties, ["prompt"])
  )
    return undefined;
  const prompt = schema.properties.prompt;
  if (
    !record(prompt) ||
    !onlyKeys(prompt, ["type", "minLength", "maxLength", "title", "description"]) ||
    prompt.type !== "string" ||
    prompt.minLength !== 1 ||
    prompt.maxLength !== 262144 ||
    (prompt.title !== undefined &&
      (typeof prompt.title !== "string" || !prompt.title.trim() || prompt.title.length > 128)) ||
    (prompt.description !== undefined &&
      (typeof prompt.description !== "string" ||
        !prompt.description.trim() ||
        prompt.description.length > 512))
  )
    return undefined;
  return {
    label: (prompt.title as string | undefined) ?? "",
    guidance: (prompt.description as string | undefined) ?? "",
  };
}
