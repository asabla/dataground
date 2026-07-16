import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
  contrastRatio,
  flattenTokens,
  generateArtifacts,
  resolveToken,
  toCssValue,
} from "../scripts/build-tokens.mjs";

describe("DataGround token generation", () => {
  it("resolves aliases without changing their declared type", () => {
    const tokens = flattenTokens({
      palette: {
        $type: "color",
        base: {
          $value: { colorSpace: "srgb", components: [0, 0, 0], hex: "#000000" },
        },
      },
      color: { $type: "color", canvas: { $value: "{palette.base}" } },
    });

    assert.equal(resolveToken("color.canvas", tokens).value.hex, "#000000");
  });

  it("rejects circular aliases", () => {
    const tokens = flattenTokens({
      color: {
        $type: "color",
        first: { $value: "{color.second}" },
        second: { $value: "{color.first}" },
      },
    });

    assert.throws(() => resolveToken("color.first", tokens), /Circular token reference/u);
  });

  it("serializes supported DTCG values deterministically", () => {
    assert.equal(
      toCssValue({ type: "dimension", value: { value: 1.25, unit: "rem" } }, "space.5"),
      "1.25rem",
    );
    assert.equal(
      toCssValue(
        {
          type: "color",
          value: { colorSpace: "srgb", components: [0.231, 0.655, 0.847], hex: "#3BA7D8" },
        },
        "palette.cyan.500",
      ),
      "#3ba7d8",
    );
  });

  it("rejects inconsistent color representations and unsupported units", () => {
    assert.throws(
      () =>
        toCssValue(
          {
            type: "color",
            value: { colorSpace: "srgb", components: [0, 0, 0], hex: "#ffffff" },
          },
          "palette.invalid",
        ),
      /inconsistent components and hex/u,
    );
    assert.throws(
      () => toCssValue({ type: "dimension", value: { value: 1, unit: "vh" } }, "space.invalid"),
      /unsupported unit/u,
    );
  });

  it("measures semantic color contrast from resolved aliases", () => {
    const tokens = flattenTokens({
      palette: {
        $type: "color",
        black: {
          $value: { colorSpace: "srgb", components: [0, 0, 0], hex: "#000000" },
        },
        white: {
          $value: { colorSpace: "srgb", components: [1, 1, 1], hex: "#ffffff" },
        },
      },
      color: {
        $type: "color",
        foreground: { $value: "{palette.black}" },
        background: { $value: "{palette.white}" },
      },
    });

    assert.equal(contrastRatio("color.foreground", "color.background", tokens), 21);
  });

  it("generates CSS, JavaScript, and declarations from one source", async () => {
    const artifacts = await generateArtifacts();

    assert.deepEqual([...artifacts.keys()], ["tokens.css", "tokens.js", "tokens.d.ts"]);
    assert.match(artifacts.get("tokens.css"), /:root\[data-theme="contrast-dark"\]/u);
    assert.match(artifacts.get("tokens.css"), /prefers-reduced-motion/u);
    assert.match(artifacts.get("tokens.d.ts"), /export type TokenName/u);
  });
});
