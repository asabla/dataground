import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

it("blocks backend connections in the preview document", () => {
  const document = readFileSync(fileURLToPath(new URL("../index.html", import.meta.url)), "utf8");
  expect(document).toContain("connect-src 'none'");
  expect(document).toContain("form-action 'none'");
  expect(document).toContain("object-src 'none'");
});

it("keeps the preview outside the production build but inside verification", () => {
  const manifest = JSON.parse(
    readFileSync(fileURLToPath(new URL("../../../package.json", import.meta.url)), "utf8"),
  );
  const visited = new Set<string>();
  const visit = (name: string) => {
    if (visited.has(name)) return;
    visited.add(name);
    const command = manifest.scripts[name] as string;
    expect(command).not.toContain("workbench-prototype");
    expect(command).not.toContain("prototype");
    for (const match of command.matchAll(/pnpm run ([\w:-]+)/g)) visit(match[1] as string);
  };
  visit("build");
  expect(manifest.scripts.verify).toContain("verify:prototype");
});
