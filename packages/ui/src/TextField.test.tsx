import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, it } from "vitest";
import { TextField } from "./TextField";

describe("TextField", () => {
  it("binds a visible label and description to a native input", () => {
    const markup = renderToStaticMarkup(
      <TextField description="Stable service alias." label="Alias" name="alias" value="stable" />,
    );

    assert.match(markup, /<label/u);
    assert.match(markup, />Alias</u);
    assert.match(markup, /<input/u);
    assert.match(markup, /name="alias"/u);
    assert.match(markup, /value="stable"/u);
    assert.match(markup, /Stable service alias/u);
  });

  it("renders multiline input and accessible validation evidence", () => {
    const markup = renderToStaticMarkup(
      <TextField
        errorMessage="Prompt is required."
        isMultiline
        isRequired
        label="Prompt"
        maxLength={256}
        minLength={1}
        name="prompt"
        value=""
      />,
    );

    assert.match(markup, /<textarea/u);
    assert.match(markup, /maxLength="256"/u);
    assert.match(markup, /minLength="1"/u);
    assert.match(markup, /aria-invalid="true"/u);
    assert.match(markup, /Prompt is required/u);
  });
});
