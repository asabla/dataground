import type { DataGroundClient } from "../contracts/client";
import type { components } from "../contracts/openapi.gen";

export type InvocationEvent = components["schemas"]["EventEnvelope"];
type ErrorEnvelope = components["schemas"]["ErrorEnvelope"];

export interface InvocationEventReference {
  invocationId: string;
  isolationDomainId: string;
}

export interface EventReplayFailure {
  code: string;
  correlationId?: string;
  message: string;
  retryable: boolean;
  status?: number;
}

export type EventReplayResult =
  | { cursor: number; events: InvocationEvent[]; ok: true }
  | { error: EventReplayFailure; ok: false };

const eventPath = "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/events";
const eventIdPattern = /^evt_[0-9a-z]{20,32}$/u;
const eventTypePattern = /^[a-z][a-z0-9]*(?:\.[a-z0-9]+)+$/u;
const resourcePatterns = {
  invocationId: /^inv_[0-9a-z]{20,32}$/u,
  isolationDomainId: /^iso_[0-9a-z]{20,32}$/u,
  revisionId: /^rev_[0-9a-z]{20,32}$/u,
  serviceId: /^svc_[0-9a-z]{20,32}$/u,
};
const MAX_REPLAY_BYTES = 1_048_576;
const MAX_REPLAY_EVENTS = 500;
const MAX_EVENT_IDENTITY_LENGTH = 128;
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/u;

function failure(code: string, message: string, retryable = false): EventReplayResult {
  return { error: { code, message, retryable }, ok: false };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isTimestamp(value: unknown): value is string {
  return (
    typeof value === "string" && timestampPattern.test(value) && !Number.isNaN(Date.parse(value))
  );
}

function matchesRequiredString(value: unknown, pattern?: RegExp): value is string {
  return (
    typeof value === "string" && value !== "" && (pattern === undefined || pattern.test(value))
  );
}

function validReference(reference: InvocationEventReference): boolean {
  return (
    resourcePatterns.invocationId.test(reference.invocationId) &&
    resourcePatterns.isolationDomainId.test(reference.isolationDomainId)
  );
}

function validExtensions(value: unknown): boolean {
  return (
    value === undefined ||
    (isRecord(value) &&
      Object.entries(value).every(
        ([key, extension]) => eventTypePattern.test(key) && isRecord(extension),
      ))
  );
}

function decodeEvent(
  value: unknown,
  reference: InvocationEventReference,
): InvocationEvent | undefined {
  if (
    !isRecord(value) ||
    value.schemaVersion !== "dataground.event/v1" ||
    !matchesRequiredString(value.id, eventIdPattern) ||
    value.isolationDomainId !== reference.isolationDomainId ||
    value.invocationId !== reference.invocationId ||
    !Number.isSafeInteger(value.sequence) ||
    (value.sequence as number) < 1 ||
    (!matchesRequiredString(value.type, eventTypePattern) &&
      value.type !== "lifecycle.cancellation-requested") ||
    !isTimestamp(value.occurredAt) ||
    !isTimestamp(value.recordedAt) ||
    !matchesRequiredString(value.correlationId) ||
    value.correlationId.length > MAX_EVENT_IDENTITY_LENGTH ||
    !matchesRequiredString(value.actorId) ||
    value.actorId.length > MAX_EVENT_IDENTITY_LENGTH ||
    !matchesRequiredString(value.serviceId, resourcePatterns.serviceId) ||
    !matchesRequiredString(value.revisionId, resourcePatterns.revisionId) ||
    !isRecord(value.payload) ||
    !validExtensions(value.extensions)
  ) {
    return undefined;
  }
  return value as InvocationEvent;
}

interface ServerSentFrame {
  data: string;
  event?: string;
  id?: string;
}

function decodeFrame(block: string): ServerSentFrame {
  const data: string[] = [];
  let event: string | undefined;
  let id: string | undefined;
  for (const line of block.split("\n")) {
    if (line === "" || line.startsWith(":")) {
      continue;
    }
    const separator = line.indexOf(":");
    const field = separator === -1 ? line : line.slice(0, separator);
    const rawValue = separator === -1 ? "" : line.slice(separator + 1);
    const value = rawValue.startsWith(" ") ? rawValue.slice(1) : rawValue;
    if (field === "data") {
      data.push(value);
    } else if (field === "event") {
      event = value;
    } else if (field === "id") {
      id = value;
    }
  }
  return { data: data.join("\n"), event, id };
}

export function parseInvocationEventStream(
  body: string,
  reference: InvocationEventReference,
  afterSequence = 0,
): EventReplayResult {
  if (!Number.isSafeInteger(afterSequence) || afterSequence < 0) {
    return failure("WORKBENCH_INVALID_CURSOR", "The event replay cursor is invalid.");
  }
  if (!validReference(reference)) {
    return failure("WORKBENCH_INVALID_REFERENCE", "The invocation event reference is invalid.");
  }
  if (new TextEncoder().encode(body).byteLength > MAX_REPLAY_BYTES) {
    return failure(
      "WORKBENCH_EVENT_REPLAY_TOO_LARGE",
      "The event replay exceeded the Workbench safety limit.",
    );
  }

  const normalized = body.replaceAll("\r\n", "\n").replaceAll("\r", "\n").trim();
  if (normalized === "") {
    return { cursor: afterSequence, events: [], ok: true };
  }
  const blocks = normalized.split(/\n\n+/u);
  if (blocks.length > MAX_REPLAY_EVENTS) {
    return failure(
      "WORKBENCH_EVENT_REPLAY_TOO_LARGE",
      "The event replay contained too many records.",
    );
  }

  const events: InvocationEvent[] = [];
  let cursor = afterSequence;
  for (const block of blocks) {
    const frame = decodeFrame(block);
    if (!frame.id || !/^[0-9]+$/u.test(frame.id) || !frame.event || frame.data === "") {
      return failure(
        "WORKBENCH_INVALID_EVENT_STREAM",
        "DataGround returned an event frame the Workbench could not interpret.",
      );
    }
    const sequence = Number(frame.id);
    if (!Number.isSafeInteger(sequence) || sequence !== cursor + 1) {
      return failure(
        "WORKBENCH_EVENT_SEQUENCE_GAP",
        "The event replay was not contiguous after the confirmed cursor.",
        true,
      );
    }
    let decoded: unknown;
    try {
      decoded = JSON.parse(frame.data);
    } catch {
      return failure(
        "WORKBENCH_INVALID_EVENT_STREAM",
        "DataGround returned event data the Workbench could not decode.",
      );
    }
    const event = decodeEvent(decoded, reference);
    if (!event || event.sequence !== sequence || event.type !== frame.event) {
      return failure(
        "WORKBENCH_EVENT_SCOPE_MISMATCH",
        "DataGround returned event data outside the requested scope or cursor.",
      );
    }
    events.push(event);
    cursor = sequence;
  }
  return { cursor, events, ok: true };
}

function failedResult(error: ErrorEnvelope | undefined, status: number): EventReplayResult {
  const problem = error?.error;
  if (problem) {
    return {
      error: {
        code: problem.code,
        correlationId: problem.correlationId,
        message: problem.message,
        retryable: problem.retryable,
        status,
      },
      ok: false,
    };
  }
  return {
    error: {
      code: "WORKBENCH_INVALID_RESPONSE",
      message: "DataGround returned an event response the Workbench could not interpret.",
      retryable: false,
      status,
    },
    ok: false,
  };
}

export async function replayInvocationEvents(
  client: DataGroundClient,
  reference: InvocationEventReference,
  afterSequence = 0,
): Promise<EventReplayResult> {
  if (!Number.isSafeInteger(afterSequence) || afterSequence < 0) {
    return failure("WORKBENCH_INVALID_CURSOR", "The event replay cursor is invalid.");
  }
  if (!validReference(reference)) {
    return failure("WORKBENCH_INVALID_REFERENCE", "The invocation event reference is invalid.");
  }
  try {
    const { data, error, response } = await client.GET(eventPath, {
      parseAs: "text",
      params: {
        ...(afterSequence > 0 ? { header: { "Last-Event-ID": String(afterSequence) } } : undefined),
        path: reference,
      },
    });
    if (typeof data === "string") {
      return parseInvocationEventStream(data, reference, afterSequence);
    }
    if (response.status === 200 && data === undefined && error === undefined) {
      return parseInvocationEventStream("", reference, afterSequence);
    }
    return failedResult(error, response.status);
  } catch {
    return {
      error: {
        code: "WORKBENCH_NETWORK_UNAVAILABLE",
        message: "The Workbench could not reach DataGround to replay invocation events.",
        retryable: true,
      },
      ok: false,
    };
  }
}
