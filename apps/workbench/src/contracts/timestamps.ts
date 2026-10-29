// Callers validate RFC 3339 timestamps before comparison. PostgreSQL and Go
// retain fractions smaller than a millisecond; Date.parse alone loses their
// order and can turn distinct creation times into an incorrect resource-ID tie.
export function compareTimestamps(left: string, right: string): number {
  const milliseconds = Date.parse(left) - Date.parse(right);
  if (milliseconds) return milliseconds;
  const fraction = (value: string) => (/\.(\d+)/u.exec(value)?.[1] ?? "").slice(3);
  const a = fraction(left),
    b = fraction(right),
    width = Math.max(a.length, b.length);
  const paddedA = a.padEnd(width, "0"),
    paddedB = b.padEnd(width, "0");
  return paddedA < paddedB ? -1 : paddedA > paddedB ? 1 : 0;
}
