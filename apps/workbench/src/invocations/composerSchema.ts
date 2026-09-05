import type { InvocationComposerField as PresentedField } from "@dataground/patterns";

export interface InvocationComposerField extends PresentedField {
  enum?: readonly string[];
  maximum?: number;
  minimum?: number;
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
const MAX_STRING_CHARACTERS = 256 * 1024;
const fieldNamePattern = /^[A-Za-z][A-Za-z0-9_-]{0,63}$/u;
const topLevelKeys = new Set([
  "$schema",
  "additionalProperties",
  "description",
  "properties",
  "required",
  "title",
  "type",
]);
const sharedKeys = ["description", "title", "type"];
const propertyKeys = {
  string: new Set([...sharedKeys, "enum", "maxLength", "minLength"]),
  number: new Set([...sharedKeys, "maximum", "minimum"]),
  integer: new Set([...sharedKeys, "maximum", "minimum"]),
  boolean: new Set(sharedKeys),
};

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
  return { error: "This input contract is not supported by the Workbench composer.", ok: false };
}

export function normalizeInvocationComposerSchema(value: unknown): InvocationComposerSchemaResult {
  if (
    !isRecord(value) ||
    !onlyKnownKeys(value, topLevelKeys) ||
    value.type !== "object" ||
    value.additionalProperties !== false ||
    !isRecord(value.properties) ||
    (value.$schema !== undefined &&
      value.$schema !== "https://json-schema.org/draft/2020-12/schema")
  )
    return unsupported();
  const properties = value.properties;
  const entries = Object.entries(properties);
  if (
    entries.length === 0 ||
    entries.length > MAX_FIELDS ||
    (value.title !== undefined && !boundedText(value.title, 128)) ||
    (value.description !== undefined && !boundedText(value.description, 512))
  )
    return unsupported();
  const required = value.required === undefined ? [] : value.required;
  if (
    !Array.isArray(required) ||
    !required.every((key): key is string => typeof key === "string") ||
    new Set(required).size !== required.length ||
    required.some((key) => !Object.hasOwn(properties, key))
  )
    return unsupported();
  const requiredFields = new Set(required);
  const fields: InvocationComposerField[] = [];
  for (const [key, property] of entries) {
    if (
      !fieldNamePattern.test(key) ||
      !isRecord(property) ||
      (property.type !== "string" &&
        property.type !== "number" &&
        property.type !== "integer" &&
        property.type !== "boolean") ||
      !onlyKnownKeys(property, propertyKeys[property.type]) ||
      (property.title !== undefined && !boundedText(property.title, 128)) ||
      (property.description !== undefined && !boundedText(property.description, 512))
    )
      return unsupported();
    const field: InvocationComposerField = {
      ...(property.description === undefined ? {} : { description: property.description }),
      key,
      label: property.title ?? key,
      required: requiredFields.has(key),
      type: property.type,
    };
    if (property.type === "string") {
      if (property.enum !== undefined) {
        if (
          !Array.isArray(property.enum) ||
          property.enum.length === 0 ||
          property.enum.length > 32 ||
          !property.enum.every(
            (entry): entry is string =>
              typeof entry === "string" && entry.length <= 128 && !entry.includes("\0"),
          ) ||
          new Set(property.enum).size !== property.enum.length
        )
          return unsupported();
        field.enum = property.enum;
        field.options = property.enum.map((entry, index) => ({
          value: String(index),
          label: entry || "(empty string)",
        }));
      }
      const minLength = property.minLength ?? 0;
      const maxLength =
        property.maxLength ??
        (field.enum && Math.max(...field.enum.map((entry) => Array.from(entry).length)));
      if (
        !Number.isSafeInteger(minLength) ||
        (minLength as number) < 0 ||
        !Number.isSafeInteger(maxLength) ||
        (maxLength as number) < 0 ||
        (maxLength as number) > MAX_STRING_CHARACTERS ||
        (minLength as number) > (maxLength as number)
      )
        return unsupported();
      field.minLength = minLength as number;
      field.maxLength = maxLength as number;
    } else if (property.type === "boolean") {
      field.options = [
        { value: "true", label: "True" },
        { value: "false", label: "False" },
      ];
    } else {
      for (const bound of ["minimum", "maximum"] as const) {
        const limit = property[bound];
        if (limit !== undefined) {
          if (
            typeof limit !== "number" ||
            !Number.isFinite(limit) ||
            Math.abs(limit) > Number.MAX_SAFE_INTEGER
          )
            return unsupported();
          field[bound] = limit;
        }
      }
      if (
        field.minimum !== undefined &&
        field.maximum !== undefined &&
        field.minimum > field.maximum
      )
        return unsupported();
    }
    fields.push(field);
  }
  return {
    ok: true,
    schema: {
      ...(value.description === undefined ? {} : { description: value.description }),
      fields,
      ...(value.title === undefined ? {} : { title: value.title }),
    },
  };
}

export type InvocationInput = Readonly<Record<string, string | number | boolean>>;
const jsonNumber = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$/u;

export function prepareInvocationInput(
  values: Readonly<Record<string, string>>,
  schema: InvocationComposerSchema,
): { errors: Record<string, string>; input: InvocationInput } {
  const errors: Record<string, string> = {};
  const entries: [string, string | number | boolean][] = [];
  for (const field of schema.fields) {
    const raw = Object.hasOwn(values, field.key) ? (values[field.key] ?? "") : "";
    if (raw === "" && !field.required) continue;
    if (raw === "" && (field.options || field.type === "number" || field.type === "integer")) {
      errors[field.key] = `${field.label} is required.`;
      continue;
    }
    let value: string | number | boolean = raw;
    if (field.enum) {
      const choice = field.options?.findIndex((option) => option.value === raw) ?? -1;
      const selected = field.enum[choice];
      if (choice < 0 || selected === undefined) {
        errors[field.key] = `Choose an option for ${field.label}.`;
        continue;
      }
      value = selected;
    } else if (field.type === "boolean") {
      if (raw !== "true" && raw !== "false") {
        errors[field.key] = `Choose true or false for ${field.label}.`;
        continue;
      }
      value = raw === "true";
    } else if (field.type === "number" || field.type === "integer") {
      const number = Number(raw);
      if (
        !jsonNumber.test(raw) ||
        !Number.isFinite(number) ||
        Math.abs(number) > Number.MAX_SAFE_INTEGER ||
        (field.type === "integer" && !Number.isSafeInteger(number))
      ) {
        errors[field.key] = `Enter a valid ${field.type} for ${field.label}.`;
        continue;
      }
      if (
        (field.minimum !== undefined && number < field.minimum) ||
        (field.maximum !== undefined && number > field.maximum)
      ) {
        errors[field.key] = `${field.label} is outside its allowed range.`;
        continue;
      }
      value = number;
    }
    if (typeof value === "string") {
      const length = Array.from(value).length;
      if (length < (field.minLength ?? 0))
        errors[field.key] =
          raw === "" ? `${field.label} is required.` : `${field.label} is too short.`;
      else if (length > (field.maxLength ?? MAX_STRING_CHARACTERS))
        errors[field.key] = `${field.label} is too long.`;
      else if (value.includes("\0"))
        errors[field.key] = `${field.label} contains unsupported characters.`;
    }
    entries.push([field.key, value]);
  }
  return { errors, input: Object.fromEntries(entries) };
}
