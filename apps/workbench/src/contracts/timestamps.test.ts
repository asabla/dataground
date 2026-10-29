import { expect, it } from "vitest";
import { compareTimestamps } from "./timestamps";

it("preserves fractional order across precisions, offsets and the Unix epoch", () => {
  for (const [older, newer] of [
    ["2026-10-29T08:00:00.000001Z", "2026-10-29T08:00:00.000002Z"],
    ["2026-10-29T08:00:00.001Z", "2026-10-29T08:00:00.001000001Z"],
    ["2026-10-29T08:00:00.999999999Z", "2026-10-29T08:00:01Z"],
    ["2026-10-29T09:00:00.000001+01:00", "2026-10-29T08:00:00.000002Z"],
    ["1969-12-31T23:59:59.999999998Z", "1969-12-31T23:59:59.999999999Z"],
  ] as const) {
    expect(compareTimestamps(older, newer)).toBeLessThan(0);
    expect(compareTimestamps(newer, older)).toBeGreaterThan(0);
  }
  for (const [left, right] of [
    ["2026-10-29T08:00:00Z", "2026-10-29T08:00:00.000000000Z"],
    ["2026-10-29T08:00:00.0012Z", "2026-10-29T09:00:00.001200000+01:00"],
  ] as const)
    expect(compareTimestamps(left, right)).toBe(0);
});
