import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { DataGroundClient } from "../contracts/client";
import { type InvocationEvent, parseInvocationEventStream, replayInvocationEvents } from "./client";

const reference = {
  invocationId: "inv_00000000000000000001",
  isolationDomainId: "iso_00000000000000000001",
};

function event(sequence: number, type = "lifecycle.started"): InvocationEvent {
  return {
    actorId: "reference-runtime",
    correlationId: "correlation-fixture",
    id: `evt_${sequence.toString(36).padStart(20, "0")}`,
    invocationId: reference.invocationId,
    isolationDomainId: reference.isolationDomainId,
    occurredAt: "2026-08-14T12:00:00Z",
    payload: { message: "Reference runtime started." },
    recordedAt: "2026-08-14T12:00:00.001Z",
    revisionId: "rev_00000000000000000001",
    schemaVersion: "dataground.event/v1",
    sequence,
    serviceId: "svc_00000000000000000001",
    type,
  };
}

function frame(value: InvocationEvent, lineEnding = "\n"): string {
  return [
    `id: ${value.sequence}`,
    `event: ${value.type}`,
    `data: ${JSON.stringify(value)}`,
    "",
    "",
  ].join(lineEnding);
}

describe("invocation event replay client", () => {
  it("parses contiguous scoped SSE frames after the supplied cursor", () => {
    const result = parseInvocationEventStream(
      `${frame(event(2), "\r\n")}${frame(event(3, "lifecycle.succeeded"))}`,
      reference,
      1,
    );

    assert.equal(result.ok, true);
    if (result.ok) {
      assert.equal(result.cursor, 3);
      assert.deepEqual(
        result.events.map((value) => value.sequence),
        [2, 3],
      );
    }
  });

  it("accepts unknown event types while preserving their envelope", () => {
    const unknown = event(1, "runtime.future.signal");
    unknown.payload = { meaning: "safe to ignore" };

    const result = parseInvocationEventStream(frame(unknown), reference);

    assert.equal(result.ok, true);
    if (result.ok) {
      assert.equal(result.events[0]?.type, "runtime.future.signal");
      assert.deepEqual(result.events[0]?.payload, { meaning: "safe to ignore" });
    }
  });

  it("replays canonical and retained cancellation requests without relaxing extension namespaces", () => {
    const names = [
      "lifecycle.accepted",
      "lifecycle.running",
      "lifecycle.cancellation-requested",
      "lifecycle.cancellation.requested",
      "lifecycle.cancelling",
      "lifecycle.cancelled",
    ];
    const result = parseInvocationEventStream(
      names.map((type, index) => frame(event(index + 1, type))).join(""),
      reference,
    );
    assert.ok(result.ok);
    assert.equal(result.cursor, 6);
    assert.deepEqual(
      result.events.map((value) => value.type),
      names,
    );
    for (const type of [
      "lifecycle.other-invalid",
      "lifecycle.cancellation--requested",
      "lifecycle.cancellation-requested.extra",
    ]) {
      assert.equal(parseInvocationEventStream(frame(event(1, type)), reference).ok, false);
    }
    const invalidExtension = {
      ...event(1),
      extensions: { "lifecycle.cancellation-requested": {} },
    };
    assert.equal(parseInvocationEventStream(frame(invalidExtension), reference).ok, false);
  });

  it("preserves trusted event origin and refuses malformed origin instead of treating it as platform work", () => {
    for (const source of [undefined, "platform", "runtime"] as const) {
      const value = { ...event(1, "lifecycle.succeeded"), source };
      const result = parseInvocationEventStream(frame(value), reference);
      assert.ok(result.ok);
      assert.equal(result.events[0]?.source, source);
    }
    for (const source of [null, "", "native-endpoint", 42]) {
      const value = { ...event(1), source } as unknown as InvocationEvent;
      assert.equal(parseInvocationEventStream(frame(value), reference).ok, false);
    }
  });

  it("rejects sequence gaps, header mismatches, and cross-scope envelopes", () => {
    const gap = parseInvocationEventStream(frame(event(3)), reference, 1);
    assert.equal(gap.ok, false);
    if (!gap.ok) {
      assert.equal(gap.error.code, "WORKBENCH_EVENT_SEQUENCE_GAP");
    }

    const headerMismatch = frame(event(1)).replace(
      "event: lifecycle.started",
      "event: lifecycle.failed",
    );
    const mismatched = parseInvocationEventStream(headerMismatch, reference);
    assert.equal(mismatched.ok, false);
    if (!mismatched.ok) {
      assert.equal(mismatched.error.code, "WORKBENCH_EVENT_SCOPE_MISMATCH");
    }

    const otherDomain = { ...event(1), isolationDomainId: "iso_00000000000000000002" };
    const scoped = parseInvocationEventStream(frame(otherDomain), reference);
    assert.equal(scoped.ok, false);
    if (!scoped.ok) {
      assert.equal(scoped.error.code, "WORKBENCH_EVENT_SCOPE_MISMATCH");
    }
  });

  it("rejects malformed timestamps, oversized actors, and unnamespaced extensions", () => {
    const malformedTime = { ...event(1), occurredAt: "tomorrow" };
    const oversizedActor = { ...event(1), actorId: "a".repeat(129) };
    const unsafeExtension = { ...event(1), extensions: { reference: { unsafe: true } } };

    for (const value of [malformedTime, oversizedActor, unsafeExtension]) {
      const result = parseInvocationEventStream(frame(value), reference);
      assert.equal(result.ok, false);
      if (!result.ok) {
        assert.equal(result.error.code, "WORKBENCH_EVENT_SCOPE_MISMATCH");
      }
    }
  });

  it("binds the complete path and Last-Event-ID header", async () => {
    let options: unknown;
    const client = {
      GET: async (_path: string, value: unknown) => {
        options = value;
        return { data: frame(event(2)), response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;

    const result = await replayInvocationEvents(client, reference, 1);

    assert.equal(result.ok, true);
    assert.deepEqual(options, {
      parseAs: "text",
      params: {
        header: { "Last-Event-ID": "1" },
        path: reference,
      },
    });
  });

  it("omits Last-Event-ID for the initial replay", async () => {
    let options: unknown;
    const client = {
      GET: async (_path: string, value: unknown) => {
        options = value;
        return { data: frame(event(1)), response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;

    const result = await replayInvocationEvents(client, reference);

    assert.equal(result.ok, true);
    assert.deepEqual(options, {
      parseAs: "text",
      params: { path: reference },
    });
  });

  it("accepts an empty successful replay after the confirmed cursor", async () => {
    const client = {
      GET: async () => ({ response: new Response(null, { status: 200 }) }),
    } as unknown as DataGroundClient;

    const result = await replayInvocationEvents(client, reference, 8);

    assert.deepEqual(result, { cursor: 8, events: [], ok: true });
  });

  it("fails closed before transport for an invalid resource reference", async () => {
    let requested = false;
    const client = {
      GET: async () => {
        requested = true;
        return { data: "", response: new Response(null, { status: 200 }) };
      },
    } as unknown as DataGroundClient;

    const result = await replayInvocationEvents(client, {
      ...reference,
      isolationDomainId: "other-domain",
    });

    assert.equal(result.ok, false);
    assert.equal(requested, false);
    if (!result.ok) {
      assert.equal(result.error.code, "WORKBENCH_INVALID_REFERENCE");
    }
  });

  it("rejects replay bodies and frame counts beyond the bounded safety limits", () => {
    const oversizedBody = parseInvocationEventStream("x".repeat(1_048_577), reference);
    const tooManyFrames = parseInvocationEventStream(
      Array.from({ length: 501 }, (_, index) => frame(event(index + 1))).join(""),
      reference,
    );

    assert.equal(oversizedBody.ok, false);
    assert.equal(tooManyFrames.ok, false);
    if (!oversizedBody.ok && !tooManyFrames.ok) {
      assert.equal(oversizedBody.error.code, "WORKBENCH_EVENT_REPLAY_TOO_LARGE");
      assert.equal(tooManyFrames.error.code, "WORKBENCH_EVENT_REPLAY_TOO_LARGE");
    }
  });

  it("preserves safe API failures and hides thrown transport details", async () => {
    const apiClient = {
      GET: async () => ({
        error: {
          error: {
            code: "RESOURCE_NOT_FOUND",
            correlationId: "cor_00000000000000000001",
            message: "Invocation was not found.",
            retryable: false,
          },
        },
        response: new Response(null, { status: 404 }),
      }),
    } as unknown as DataGroundClient;
    const apiResult = await replayInvocationEvents(apiClient, reference);
    assert.equal(apiResult.ok, false);
    if (!apiResult.ok) {
      assert.equal(apiResult.error.code, "RESOURCE_NOT_FOUND");
      assert.equal(apiResult.error.status, 404);
    }

    const transportClient = {
      GET: async () => {
        throw new Error("secret upstream details");
      },
    } as unknown as DataGroundClient;
    const transportResult = await replayInvocationEvents(transportClient, reference);
    assert.equal(transportResult.ok, false);
    if (!transportResult.ok) {
      assert.equal(transportResult.error.code, "WORKBENCH_NETWORK_UNAVAILABLE");
      assert.doesNotMatch(transportResult.error.message, /secret upstream details/u);
    }
  });
});
