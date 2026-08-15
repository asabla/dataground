import { spawnSync } from "node:child_process";
import {
  createPrivateKey,
  createPublicKey,
  generateKeyPairSync,
  randomBytes,
  timingSafeEqual,
} from "node:crypto";
import {
  closeSync,
  constants,
  existsSync,
  fstatSync,
  fsyncSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  realpathSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { homedir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const expectedOpenShellVersion = "openshell 0.0.86";
const composeArguments = [
  "compose",
  "--project-name",
  "dataground-openshell-local",
  "--file",
  "deploy/openshell/local/docker-compose.yml",
];
const gatewayJWTFiles = ["signing.pem", "public.pem", "kid"];

function fail(message) {
  throw new Error(message);
}

function requirePrivateDirectory(path) {
  const info = lstatSync(path);
  if (info.isSymbolicLink() || !info.isDirectory() || (info.mode & 0o777) !== 0o700) {
    fail("OpenShell local state directory must be a direct mode-0700 directory");
  }
  if (realpathSync(path) !== path) {
    fail("OpenShell local state directory must not traverse symbolic links");
  }
}

function syncDirectory(path) {
  const descriptor = openSync(path, constants.O_RDONLY);
  try {
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

function writePrivateFile(path, content) {
  if (typeof constants.O_NOFOLLOW !== "number") {
    fail("this platform cannot enforce no-follow local key writes");
  }
  const descriptor = openSync(
    path,
    constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
    0o600,
  );
  try {
    writeFileSync(descriptor, content);
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

function readPrivateFile(path) {
  if (typeof constants.O_NOFOLLOW !== "number") {
    fail("this platform cannot enforce no-follow local key reads");
  }
  const descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const info = fstatSync(descriptor);
    if (!info.isFile() || (info.mode & 0o777) !== 0o600 || info.size < 1 || info.size > 4096) {
      fail("OpenShell local key file is invalid");
    }
    return readFileSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

export function validateLocalGatewayJWT(path) {
  requirePrivateDirectory(path);
  for (const name of gatewayJWTFiles) {
    const info = lstatSync(join(path, name));
    if (info.isSymbolicLink()) {
      fail("OpenShell local key files must not be symbolic links");
    }
  }

  const privatePEM = readPrivateFile(join(path, "signing.pem"));
  const publicPEM = readPrivateFile(join(path, "public.pem"));
  const kid = readPrivateFile(join(path, "kid"));
  try {
    if (!/^[a-f0-9]{32}\n$/u.test(kid.toString("utf8"))) {
      fail("OpenShell local gateway key identifier is invalid");
    }
    const privateKey = createPrivateKey(privatePEM);
    const suppliedPublicKey = createPublicKey(publicPEM).export({ format: "der", type: "spki" });
    const derivedPublicKey = createPublicKey(privateKey).export({ format: "der", type: "spki" });
    if (
      suppliedPublicKey.length !== derivedPublicKey.length ||
      !timingSafeEqual(suppliedPublicKey, derivedPublicKey)
    ) {
      fail("OpenShell local gateway keypair does not match");
    }
  } finally {
    privatePEM.fill(0);
  }
  return path;
}

export function ensureLocalGatewayJWT(stateRoot) {
  mkdirSync(stateRoot, { recursive: true, mode: 0o700 });
  requirePrivateDirectory(stateRoot);
  const jwtPath = join(stateRoot, "gateway-jwt");
  if (existsSync(jwtPath)) {
    return validateLocalGatewayJWT(jwtPath);
  }

  const temporaryPath = mkdtempSync(join(stateRoot, "gateway-jwt.tmp-"));
  try {
    requirePrivateDirectory(temporaryPath);
    const { privateKey, publicKey } = generateKeyPairSync("ed25519");
    const privatePEM = Buffer.from(privateKey.export({ format: "pem", type: "pkcs8" }));
    try {
      writePrivateFile(join(temporaryPath, "signing.pem"), privatePEM);
    } finally {
      privatePEM.fill(0);
    }
    writePrivateFile(
      join(temporaryPath, "public.pem"),
      publicKey.export({ format: "pem", type: "spki" }),
    );
    writePrivateFile(join(temporaryPath, "kid"), `${randomBytes(16).toString("hex")}\n`);
    syncDirectory(temporaryPath);
    try {
      renameSync(temporaryPath, jwtPath);
      syncDirectory(stateRoot);
    } catch (error) {
      if (error?.code !== "EEXIST" && error?.code !== "ENOTEMPTY") {
        throw error;
      }
    }
  } finally {
    if (existsSync(temporaryPath)) {
      rmSync(temporaryPath, { recursive: true });
    }
  }
  return validateLocalGatewayJWT(jwtPath);
}

function defaultStateRoot() {
  return join(realpathSync(homedir()), ".local", "state", "dataground", "openshell-local");
}

function requirePinnedOpenShell() {
  const result = spawnSync("openshell", ["--version"], {
    encoding: "utf8",
    shell: false,
  });
  if (result.error || result.status !== 0 || result.stdout.trim() !== expectedOpenShellVersion) {
    fail(`OpenShell ${expectedOpenShellVersion.slice("openshell ".length)} is required`);
  }
}

function runCompose(action, jwtPath) {
  const composeAction = action === "up" ? ["up", "--detach"] : ["down", "--remove-orphans"];
  const result = spawnSync("docker", [...composeArguments, ...composeAction], {
    env: { ...process.env, DATAGROUND_OPENSHELL_LOCAL_JWT_PATH: jwtPath },
    shell: false,
    stdio: "inherit",
  });
  if (result.error || result.status !== 0) {
    fail(`Docker Compose ${action} failed`);
  }
}

function main() {
  const action = process.argv[2];
  if (action !== "up" && action !== "down") {
    fail("usage: node scripts/openshell-local.mjs <up|down>");
  }
  const stateRoot = defaultStateRoot();
  if (action === "up") {
    requirePinnedOpenShell();
  }
  const jwtPath =
    action === "up" ? ensureLocalGatewayJWT(stateRoot) : join(stateRoot, "gateway-jwt");
  runCompose(action, jwtPath);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  try {
    main();
  } catch (error) {
    console.error(`OpenShell local development command failed: ${error.message}`);
    process.exitCode = 1;
  }
}
