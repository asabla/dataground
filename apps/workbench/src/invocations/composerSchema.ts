export interface InvocationComposerField {
  description?: string;
  key: string;
  label: string;
  maxLength: number;
  minLength: number;
  required: boolean;
}

export interface InvocationComposerSchema {
  description?: string;
  fields: InvocationComposerField[];
  title?: string;
}

export type InvocationComposerSchemaResult =
  | { ok: true; schema: InvocationComposerSchema }
  | { error: string; ok: false };

const MAX_FIELDS = 8;
const MAX_PROMPT_BYTES = 256 * 1024;
const fieldNamePattern = /^[A-Za-z][A-Za-z0-9_-]{0,63}$/u;
const topLevelKeys = new Set([
  "additionalProperties",
  "description",
  "properties",
  "required",
  "title",
  "type",
]);
const propertyKeys = new Set(["description", "maxLength", "minLength", "title", "type"]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function boundedText(value: unknown, maximum: number): value is string {
  return typeof value === "string" && value.trim().length > 0 && value.length <= maximum;
}

function onlyKnownKeys(value: Record<string, unknown>, allowed: ReadonlySet<string>): boolean {
  return Object.keys(value).every((key) => allowed.has(key));
}

function unsupported(): InvocationComposerSchemaResult {
  return {
    error: "This input contract is not supported by the Workbench composer.",
    ok: false,
  };
}

export function normalizeInvocationComposerSchema(value: unknown): InvocationComposerSchemaResult {
  if (
    !isRecord(value) ||
    !onlyKnownKeys(value, topLevelKeys) ||
    value.type !== "object" ||
    value.additionalProperties !== false ||
    !isRecord(value.properties)
  ) {
    return unsupported();
  }

  const properties = value.properties;
  const entries = Object.entries(properties);
  if (entries.length === 0 || entries.length > MAX_FIELDS) {
    return unsupported();
  }

  if (value.title !== undefined && !boundedText(value.title, 128)) {
    return unsupported();
  }
  if (value.description !== undefined && !boundedText(value.description, 512)) {
    return unsupported();
  }

  const required = value.required === undefined ? [] : value.required;
  if (
    !Array.isArray(required) ||
    !required.every((key): key is string => typeof key === "string") ||
    new Set(required).size !== required.length ||
    required.some((key) => !(key in properties))
  ) {
    return unsupported();
  }
  const requiredFields = new Set(required);
  const fields: InvocationComposerField[] = [];

  for (const [key, property] of entries) {
    if (
      !fieldNamePattern.test(key) ||
      !isRecord(property) ||
      !onlyKnownKeys(property, propertyKeys) ||
      property.type !== "string" ||
      (property.title !== undefined && !boundedText(property.title, 128)) ||
      (property.description !== undefined && !boundedText(property.description, 512))
    ) {
      return unsupported();
    }
    const minLength = property.minLength === undefined ? 0 : property.minLength;
    const maxLength = property.maxLength;
    if (
      !Number.isSafeInteger(minLength) ||
      (minLength as number) < 0 ||
      !Number.isSafeInteger(maxLength) ||
      (maxLength as number) < 1 ||
      (maxLength as number) > MAX_PROMPT_BYTES ||
      (minLength as number) > (maxLength as number)
    ) {
      return unsupported();
    }
    fields.push({
      ...(property.description === undefined ? undefined : { description: property.description }),
      key,
      label: property.title ?? key,
      maxLength: maxLength as number,
      minLength: minLength as number,
      required: requiredFields.has(key),
    });
  }

  return {
    ok: true,
    schema: {
      ...(value.description === undefined ? undefined : { description: value.description }),
      fields,
      ...(value.title === undefined ? undefined : { title: value.title }),
    },
  };
}
