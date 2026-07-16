import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { copyFile, mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const source = path.join(repositoryRoot, "contracts/openapi/dataground-api.openapi.json");
const outputs = {
  typescript: path.join(repositoryRoot, "apps/workbench/src/contracts/openapi.gen.ts"),
};
const write = process.argv.includes("--write");
const temporaryDirectory = await mkdtemp(path.join(tmpdir(), "dataground-contracts-"));

async function generate() {
  const generated = {
    typescript: path.join(temporaryDirectory, "openapi.gen.ts"),
  };

  await execFileAsync(
    "pnpm",
    ["exec", "openapi-typescript", source, "--output", generated.typescript],
    { cwd: repositoryRoot },
  );

  for (const [language, generatedPath] of Object.entries(generated)) {
    if (write) {
      await mkdir(path.dirname(outputs[language]), { recursive: true });
      await copyFile(generatedPath, outputs[language]);
      continue;
    }

    const [actual, expected] = await Promise.all([
      readFile(outputs[language], "utf8"),
      readFile(generatedPath, "utf8"),
    ]);
    assert.equal(
      actual,
      expected,
      `${language} contract types are stale; run pnpm contracts:generate`,
    );
  }
}

try {
  await generate();
  console.log(write ? "contract types generated" : "generated contract types are current");
} finally {
  await rm(temporaryDirectory, { recursive: true, force: true });
}
