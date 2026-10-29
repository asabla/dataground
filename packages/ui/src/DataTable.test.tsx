import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { Button } from "./Button";
import { DataTable } from "./DataTable";

const items = [
  { id: "alpha", name: "Alpha", unavailable: false },
  { id: "beta", name: "<script>Beta</script>", unavailable: true },
];
const columns = [
  {
    id: "name",
    title: "Name",
    isRowHeader: true,
    render: (item: (typeof items)[number]) => item.name,
  },
  {
    id: "action",
    title: "Action",
    render: (item: (typeof items)[number]) => (
      <Button isDisabled={item.unavailable}>Open {item.name}</Button>
    ),
  },
];
const props = {
  label: "Resources",
  items,
  columns,
  getRowId: (item: (typeof items)[number]) => item.id,
  getRowLabel: (item: (typeof items)[number]) => item.name,
};

describe("DataTable", () => {
  it("provides named table and row-header semantics without enabling selection", () => {
    const markup = renderToStaticMarkup(<DataTable {...props} />);
    assert.match(markup, /<table[^>]+aria-label="Resources"/u);
    assert.match(markup, /role="rowheader"/u);
    assert.doesNotMatch(markup, /aria-selected/u);
    assert.match(markup, /&lt;script&gt;Beta&lt;\/script&gt;/u);
    assert.doesNotMatch(markup, /<script>/u);
    assert.match(markup, /<button[^>]+disabled/u);
  });

  it("does not turn a pending read into an empty-result claim", () => {
    const loading = renderToStaticMarkup(
      <DataTable {...props} items={[]} isLoading emptyMessage="Nothing found" />,
    );
    assert.match(loading, /aria-busy="true"/u);
    assert.match(loading, /Loading resources/u);
    assert.doesNotMatch(loading, /Nothing found/u);
    const empty = renderToStaticMarkup(
      <DataTable {...props} items={[]} emptyMessage="Nothing found" />,
    );
    assert.match(empty, /Nothing found/u);
  });
});
