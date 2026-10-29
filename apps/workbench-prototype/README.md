# Workbench design prototype

An isolated React app for review of the full workbench redesign. See the [design specification](../../docs/design/workbench-redesign/README.md) and its six journey scripts.

Use the repository's pinned toolchain and `pnpm install --frozen-lockfile`. Start with `pnpm dev:prototype`; open <http://127.0.0.1:5174/>. Run `pnpm verify:prototype` for the sample-state tests, type check and preview bundle build.

The app uses fictional fixtures and in-memory state. It has no API client, proxy, credentials, external assets or persistent storage. Its content security policy blocks network connections. Vite hot reload is disabled, so reload after source changes. Refresh resets the sample. Do not enter actual credentials or private content.

The production `pnpm build` does not build or import this app. The repository verification command checks the prototype separately. Prototype tests establish sample navigation/state boundaries only. They do not test real authorization, execution, retention, accessibility conformance or release readiness. Production migration requires review of this complete prototype.
