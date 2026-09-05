import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { SelectField } from "./SelectField";

describe("SelectField", () => {
  it("associates its label, help, and error with a native selection control", () => {
    const markup = renderToStaticMarkup(
      <SelectField
        description="Choose whether to include details."
        errorMessage="Choose a value."
        isRequired
        label="Details"
        name="details"
        options={[{ label: "False", value: "false" }]}
        value="false"
      />,
    );
    const id = /<select[^>]* id="([^"]+)"/u.exec(markup)?.[1];
    assert.ok(id);
    assert.ok(markup.includes(`for="${id}"`));
    assert.ok(markup.includes(`aria-describedby="${id}-description ${id}-error"`));
    assert.match(markup, /aria-invalid="true"/u);
    assert.match(markup, /required=""/u);
    assert.match(markup, /value="false" selected=""/u);
  });

  it("keeps disabled controls native and escapes option labels", () => {
    const markup = renderToStaticMarkup(
      <SelectField
        isDisabled
        label="Format"
        options={[{ label: "<script>unsafe</script>", value: "0" }]}
        value=""
      />,
    );
    assert.match(markup, /disabled=""/u);
    assert.match(markup, /&lt;script&gt;/u);
    assert.doesNotMatch(markup, /<script>/u);
  });
});
