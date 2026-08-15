import { spawnSync } from "node:child_process";
import { realpathSync } from "node:fs";
import { tmpdir } from "node:os";

const environment = { ...process.env };
const canonicalTemporaryDirectory = realpathSync(
  process.platform === "darwin" ? "/private/tmp" : tmpdir(),
);
if (process.platform === "win32") {
  environment.TEMP = canonicalTemporaryDirectory;
  environment.TMP = canonicalTemporaryDirectory;
} else {
  environment.TMPDIR = canonicalTemporaryDirectory;
}

const result = spawnSync("go", ["test", "./..."], {
  env: environment,
  shell: false,
  stdio: "inherit",
});
if (result.error) {
  throw result.error;
}
if (result.status !== 0) {
  process.exitCode = result.status ?? 1;
}
