import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { it } from "vitest";
import { ResourceAudit } from "./ResourceAudit";

it("renders safe audit facts as text and distinguishes an empty snapshot from no read", () => {
  const props = { title: "Invocation audit", resourceId: "inv_00000000000000000001" };
  assert.doesNotMatch(renderToStaticMarkup(<ResourceAudit {...props} />), /No audit records/);
  assert.match(
    renderToStaticMarkup(
      <ResourceAudit {...props} items={[]} receiptId="arr_00000000000000000001" />,
    ),
    /No audit records in this snapshot/,
  );
  const html = renderToStaticMarkup(
    <ResourceAudit
      {...props}
      receiptId="arr_00000000000000000001"
      items={[
        {
          id: "aud_00000000000000000001",
          source: "lifecycle",
          recordedAt: "2026-09-17T12:00:00Z",
          actorId: "<script>untrusted</script>",
          action: "invocation.accepted",
          outcome: "accepted",
          correlationId: "cor_00000000000000000001",
        },
      ]}
    />,
  );
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /Hide audit/);
  assert.match(html, /End of this audit snapshot/);
});
