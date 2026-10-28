export type ContractPreview =
  | { state: "absent" }
  | { state: "unavailable" }
  | { state: "ready"; text: string };

export function prepareContractPreview(
  schema: Record<string, unknown> | undefined,
): ContractPreview {
  if (schema === undefined) return { state: "absent" };
  try {
    const encoder = new TextEncoder();
    const compact = JSON.stringify(schema);
    if (typeof compact !== "string" || encoder.encode(compact).byteLength > 1 << 20)
      return { state: "unavailable" };
    const formatted = JSON.stringify(schema, null, 2);
    return {
      state: "ready",
      text: encoder.encode(formatted).byteLength <= 1 << 20 ? formatted : compact,
    };
  } catch {
    return { state: "unavailable" };
  }
}
