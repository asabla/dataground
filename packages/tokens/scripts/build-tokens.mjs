import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const packageRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const sourceRoot = path.join(packageRoot, "src");
const generatedRoot = path.join(sourceRoot, "generated");

const themeNames = ["night", "light", "contrast-dark", "contrast-light"];
const densityNames = ["compact", "standard", "comfortable"];

async function readJson(relativePath) {
  return JSON.parse(await readFile(path.join(sourceRoot, relativePath), "utf8"));
}

function assertValidName(name, parentPath) {
  if (name.startsWith("$") || /[{}.]/u.test(name)) {
    throw new Error(`Invalid token name ${[...parentPath, name].join(".")}`);
  }
}

export function flattenTokens(node, parentPath = [], inheritedType, tokens = new Map()) {
  if (!node || typeof node !== "object" || Array.isArray(node)) {
    throw new Error(`Token group ${parentPath.join(".") || "<root>"} must be an object`);
  }

  const type = node.$type ?? inheritedType;
  if (Object.hasOwn(node, "$value")) {
    if (parentPath.length === 0) throw new Error("Root token is not supported");
    if (!type) throw new Error(`Token ${parentPath.join(".")} has no type`);
    tokens.set(parentPath.join("."), {
      description: node.$description,
      type,
      value: node.$value,
    });
    return tokens;
  }

  for (const [name, child] of Object.entries(node)) {
    if (name.startsWith("$")) continue;
    assertValidName(name, parentPath);
    flattenTokens(child, [...parentPath, name], type, tokens);
  }
  return tokens;
}

function aliasPath(value) {
  if (typeof value !== "string") return null;
  const match = /^\{([^{}]+)\}$/u.exec(value);
  return match?.[1] ?? null;
}

export function resolveToken(tokenPath, tokens, resolving = new Set()) {
  const token = tokens.get(tokenPath);
  if (!token) throw new Error(`Unknown token reference ${tokenPath}`);
  if (resolving.has(tokenPath)) {
    throw new Error(`Circular token reference ${[...resolving, tokenPath].join(" -> ")}`);
  }

  const reference = aliasPath(token.value);
  if (!reference) return token;

  const nextResolving = new Set(resolving).add(tokenPath);
  const resolved = resolveToken(reference, tokens, nextResolving);
  if (resolved.type !== token.type) {
    throw new Error(
      `Token ${tokenPath} has type ${token.type} but references ${reference} (${resolved.type})`,
    );
  }
  return { ...token, value: resolved.value };
}

function assertFiniteNumber(value, tokenPath) {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new Error(`Token ${tokenPath} must contain a finite number`);
  }
}

function hexFromSrgbComponents(components) {
  return `#${components
    .map((component) =>
      Math.round(component * 255)
        .toString(16)
        .padStart(2, "0"),
    )
    .join("")}`;
}

export function toCssValue(token, tokenPath) {
  switch (token.type) {
    case "color": {
      if (
        !token.value ||
        typeof token.value !== "object" ||
        token.value.colorSpace !== "srgb" ||
        !Array.isArray(token.value.components) ||
        token.value.components.length !== 3 ||
        typeof token.value.hex !== "string" ||
        !/^#[0-9a-f]{6}$/iu.test(token.value.hex)
      ) {
        throw new Error(`Color token ${tokenPath} must use sRGB components and a six-digit hex`);
      }
      for (const component of token.value.components) {
        assertFiniteNumber(component, tokenPath);
        if (component < 0 || component > 1) {
          throw new Error(`Color token ${tokenPath} has an out-of-range component`);
        }
      }
      const normalizedHex = token.value.hex.toLowerCase();
      if (hexFromSrgbComponents(token.value.components) !== normalizedHex) {
        throw new Error(`Color token ${tokenPath} has inconsistent components and hex`);
      }
      return normalizedHex;
    }
    case "dimension":
    case "duration": {
      if (!token.value || typeof token.value !== "object" || typeof token.value.unit !== "string") {
        throw new Error(`Token ${tokenPath} must use a value/unit object`);
      }
      assertFiniteNumber(token.value.value, tokenPath);
      const allowedUnits =
        token.type === "duration" ? new Set(["ms", "s"]) : new Set(["px", "rem"]);
      if (!allowedUnits.has(token.value.unit)) {
        throw new Error(`Token ${tokenPath} uses unsupported unit ${token.value.unit}`);
      }
      return `${token.value.value}${token.value.unit}`;
    }
    case "fontFamily": {
      const families = Array.isArray(token.value) ? token.value : [token.value];
      if (families.length === 0 || families.some((family) => typeof family !== "string")) {
        throw new Error(`Font family token ${tokenPath} must contain strings`);
      }
      const genericFamilies = new Set([
        "cursive",
        "fantasy",
        "monospace",
        "sans-serif",
        "serif",
        "system-ui",
        "ui-monospace",
        "ui-sans-serif",
        "ui-serif",
      ]);
      return families
        .map((family) =>
          genericFamilies.has(family) ? family : `"${family.replaceAll('"', '\\"')}"`,
        )
        .join(", ");
    }
    case "fontWeight":
      assertFiniteNumber(token.value, tokenPath);
      if (!Number.isInteger(token.value) || token.value < 1 || token.value > 1000) {
        throw new Error(`Font weight token ${tokenPath} must be an integer from 1 through 1000`);
      }
      return String(token.value);
    case "number":
      assertFiniteNumber(token.value, tokenPath);
      return String(token.value);
    default:
      throw new Error(`Unsupported token type ${token.type} at ${tokenPath}`);
  }
}

function relativeLuminance(components) {
  const [red, green, blue] = components.map((component) =>
    component <= 0.04045 ? component / 12.92 : ((component + 0.055) / 1.055) ** 2.4,
  );
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}

export function contrastRatio(firstPath, secondPath, tokens) {
  const first = resolveToken(firstPath, tokens);
  const second = resolveToken(secondPath, tokens);
  if (first.type !== "color" || second.type !== "color") {
    throw new Error(`Contrast pair ${firstPath} and ${secondPath} must contain colors`);
  }
  const firstLuminance = relativeLuminance(first.value.components);
  const secondLuminance = relativeLuminance(second.value.components);
  const lighter = Math.max(firstLuminance, secondLuminance);
  const darker = Math.min(firstLuminance, secondLuminance);
  return (lighter + 0.05) / (darker + 0.05);
}

function assertThemeContrast(themeName, tokens) {
  const normalTextPairs = [
    ["color.text.primary", "color.canvas"],
    ["color.text.primary", "color.surface.default"],
    ["color.text.secondary", "color.canvas"],
    ["color.text.muted", "color.canvas"],
    ["color.action.primary", "color.canvas"],
    ["color.text.onAccent", "color.action.primary"],
    ["color.status.waiting", "color.status.waitingSurface"],
    ["color.status.success", "color.status.successSurface"],
    ["color.status.critical", "color.status.criticalSurface"],
  ];
  for (const [foreground, background] of normalTextPairs) {
    const ratio = contrastRatio(foreground, background, tokens);
    if (ratio < 4.5) {
      throw new Error(
        `Theme ${themeName} contrast ${foreground} on ${background} is ${ratio.toFixed(2)}:1; expected at least 4.5:1`,
      );
    }
  }
}

function toCssName(tokenPath) {
  return `--dg-${tokenPath
    .replaceAll(/([a-z0-9])([A-Z])/gu, "$1-$2")
    .replaceAll(".", "-")
    .toLowerCase()}`;
}

function declarations(tokenPaths, tokens) {
  return tokenPaths
    .toSorted()
    .map((tokenPath) => {
      const resolved = resolveToken(tokenPath, tokens);
      return `  ${toCssName(tokenPath)}: ${toCssValue(resolved, tokenPath)};`;
    })
    .join("\n");
}

function block(selector, tokenPaths, tokens, extraDeclarations = []) {
  const lines = [declarations(tokenPaths, tokens), ...extraDeclarations.map((line) => `  ${line}`)]
    .filter(Boolean)
    .join("\n");
  return `${selector} {\n${lines}\n}`;
}

function assertMatchingTokenSets(kind, namedTokenSets) {
  const [baselineName, baselineTokens] = namedTokenSets[0];
  const baseline = [...baselineTokens.keys()].toSorted();
  for (const [name, tokens] of namedTokenSets.slice(1)) {
    const candidate = [...tokens.keys()].toSorted();
    if (JSON.stringify(candidate) !== JSON.stringify(baseline)) {
      throw new Error(`${kind} ${name} does not match ${baselineName}'s token paths`);
    }
  }
  return baseline;
}

export async function generateArtifacts() {
  const foundation = flattenTokens(await readJson("foundation.tokens.json"));
  const themes = await Promise.all(
    themeNames.map(async (name) => [
      name,
      flattenTokens(await readJson(`themes/${name}.tokens.json`)),
    ]),
  );
  const densities = await Promise.all(
    densityNames.map(async (name) => [
      name,
      flattenTokens(await readJson(`densities/${name}.tokens.json`)),
    ]),
  );

  const themePaths = assertMatchingTokenSets("Theme", themes);
  const densityPaths = assertMatchingTokenSets("Density", densities);
  const foundationPaths = [...foundation.keys()].toSorted();

  for (const [name, tokens] of themes) {
    assertThemeContrast(name, new Map([...foundation, ...tokens]));
  }

  const css = [
    "/* Generated by packages/tokens/scripts/build-tokens.mjs. Do not edit. */",
    block(":root", foundationPaths, foundation),
    block(":root", themePaths, new Map([...foundation, ...themes[0][1]]), ["color-scheme: dark;"]),
    block(":root", densityPaths, densities[0][1]),
    ...themes.map(([name, tokens]) =>
      block(
        `:root[data-theme="${name}"], [data-theme="${name}"]`,
        themePaths,
        new Map([...foundation, ...tokens]),
        [`color-scheme: ${name.includes("light") ? "light" : "dark"};`],
      ),
    ),
    "@media (prefers-color-scheme: light) {\n" +
      block(
        "  :root:not([data-theme])",
        themePaths,
        new Map([...foundation, ...themes.find(([name]) => name === "light")[1]]),
        ["color-scheme: light;"],
      ) +
      "\n}",
    ...densities.map(([name, tokens]) =>
      block(`:root[data-density="${name}"], [data-density="${name}"]`, densityPaths, tokens),
    ),
    "@media (prefers-reduced-motion: reduce) {\n  :root {\n    --dg-motion-duration-fast: 0ms;\n    --dg-motion-duration-standard: 0ms;\n  }\n}",
    "",
  ].join("\n\n");

  const tokenNames = [...new Set([...foundationPaths, ...themePaths, ...densityPaths])].toSorted();
  const js = [
    "// Generated by packages/tokens/scripts/build-tokens.mjs. Do not edit.",
    `export const tokenNames = Object.freeze(${JSON.stringify(tokenNames, null, 2)});`,
    "",
    "export function cssVariable(tokenName) {",
    '  if (!tokenNames.includes(tokenName)) throw new TypeError(["Unknown DataGround token: ", tokenName].join(""));',
    '  return ["--dg-", tokenName.replace(/([a-z0-9])([A-Z])/g, "$1-$2").replaceAll(".", "-").toLowerCase()].join("");',
    "}",
    "",
  ].join("\n");
  const union = tokenNames.map((name) => JSON.stringify(name)).join(" | ");
  const declarationsFile = [
    "// Generated by packages/tokens/scripts/build-tokens.mjs. Do not edit.",
    `export type TokenName = ${union};`,
    "export declare const tokenNames: readonly TokenName[];",
    "export declare function cssVariable(tokenName: TokenName): string;",
    "",
  ].join("\n");

  return new Map([
    ["tokens.css", css],
    ["tokens.js", js],
    ["tokens.d.ts", declarationsFile],
  ]);
}

async function writeArtifacts(artifacts) {
  for (const [filename, contents] of artifacts) {
    await writeFile(path.join(generatedRoot, filename), contents);
  }
}

async function checkArtifacts(artifacts) {
  const stale = [];
  for (const [filename, expected] of artifacts) {
    let actual;
    try {
      actual = await readFile(path.join(generatedRoot, filename), "utf8");
    } catch {
      stale.push(filename);
      continue;
    }
    if (actual !== expected) stale.push(filename);
  }
  if (stale.length > 0) {
    throw new Error(
      `Generated token artifacts are stale: ${stale.join(", ")}. Run pnpm tokens:generate.`,
    );
  }
}

async function main() {
  const mode = process.argv[2];
  if (mode !== "--write" && mode !== "--check") {
    throw new Error("Expected --write or --check");
  }
  const artifacts = await generateArtifacts();
  if (mode === "--write") await writeArtifacts(artifacts);
  else await checkArtifacts(artifacts);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main();
}
