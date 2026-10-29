import { Button, SelectField, StatusBadge, TextField } from "@dataground/ui";
import { useState } from "react";
import { createDataGroundClient, type DataGroundClient } from "./contracts/client";
import { DevelopmentWorkbench } from "./workbench/DevelopmentWorkbench";

import type { WorkbenchPresentation } from "./workbench/navigation";

export { DevelopmentWorkbench } from "./workbench/DevelopmentWorkbench";

const DEFAULT_ISOLATION_DOMAIN_ID = "iso_00000000000000000001";
const ISOLATION_DOMAIN_ID_PATTERN = /^iso_[0-9a-z]{20,32}$/u;
const BEARER_TOKEN_PATTERN = /^[A-Za-z0-9\-._~+/]+=*$/u;

interface DevelopmentScopeErrors {
  bearerToken?: string;
  isolationDomainId?: string;
}

interface DevelopmentSession {
  presentation: WorkbenchPresentation;
  client: DataGroundClient;
  isolationDomainId: string;
}

export function validateDevelopmentScope(
  isolationDomainId: string,
  bearerToken: string,
): DevelopmentScopeErrors {
  const errors: DevelopmentScopeErrors = {};
  const normalizedIsolationDomainId = isolationDomainId.trim();
  if (!ISOLATION_DOMAIN_ID_PATTERN.test(normalizedIsolationDomainId)) {
    errors.isolationDomainId =
      "Use an isolation domain identifier beginning with iso_ and 20 to 32 lowercase letters or digits.";
  }

  const tokenLength = new TextEncoder().encode(bearerToken).byteLength;
  if (tokenLength < 32) {
    errors.bearerToken = "The development bearer token must be at least 32 bytes.";
  } else if (tokenLength > 8192) {
    errors.bearerToken = "The development bearer token must not exceed 8,192 bytes.";
  } else if (!BEARER_TOKEN_PATTERN.test(bearerToken)) {
    errors.bearerToken = "The token contains characters that cannot be sent in a Bearer header.";
  }
  return errors;
}

export function App() {
  const [presentation, setPresentation] = useState<WorkbenchPresentation>("workbench");
  const [bearerToken, setBearerToken] = useState("");
  const [isolationDomainId, setIsolationDomainId] = useState(DEFAULT_ISOLATION_DOMAIN_ID);
  const [session, setSession] = useState<DevelopmentSession>();
  const [validationErrors, setValidationErrors] = useState<DevelopmentScopeErrors>({});

  if (session) {
    return (
      <DevelopmentWorkbench
        presentation={session.presentation}
        client={session.client}
        isolationDomainId={session.isolationDomainId}
        onDisconnect={() => {
          setSession(undefined);
          setBearerToken("");
        }}
      />
    );
  }

  return (
    <main className="connection-shell">
      <header className="connection-brand">
        <span aria-hidden="true" className="workbench-brand__mark">
          D
        </span>
        <p className="workbench-kicker">Self-hosted data and agent platform</p>
        <h1>DataGround</h1>
        <p>Open a local development scope to build and operate governed agent services.</p>
      </header>

      <section className="development-connection" aria-labelledby="development-connection-title">
        <div>
          <StatusBadge tone="warning">Development only</StatusBadge>
          <h2 id="development-connection-title">Connect to the reference API</h2>
          <p>
            Use the development identity configured for your local API. Your token is kept only for
            this connection and is cleared when you disconnect.
          </p>
        </div>
        <form
          className="development-connection__form"
          onSubmit={(event) => {
            event.preventDefault();
            const errors = validateDevelopmentScope(isolationDomainId, bearerToken);
            setValidationErrors(errors);
            if (Object.keys(errors).length > 0) return;
            setSession({
              presentation,
              client: createDataGroundClient("", { bearerToken }),
              isolationDomainId: isolationDomainId.trim(),
            });
            setBearerToken("");
          }}
        >
          <TextField
            description="Must match the development API configuration exactly."
            errorMessage={validationErrors.isolationDomainId}
            isRequired
            label="Isolation domain"
            maxLength={36}
            name="isolation-domain-id"
            onChange={(value) => {
              setIsolationDomainId(value);
              setValidationErrors((current) => ({
                ...current,
                isolationDomainId: undefined,
              }));
            }}
            spellCheck="false"
            value={isolationDomainId}
          />
          <TextField
            autoComplete="off"
            description="Never persisted by the Workbench or included in diagnostics."
            errorMessage={validationErrors.bearerToken}
            isRequired
            label="Development bearer token"
            maxLength={8192}
            name="development-bearer-token"
            onChange={(value) => {
              setBearerToken(value);
              setValidationErrors((current) => ({ ...current, bearerToken: undefined }));
            }}
            spellCheck="false"
            type="password"
            value={bearerToken}
          />
          <SelectField
            label="Presentation"
            value={presentation}
            options={[
              { value: "workbench", label: "Workbench" },
              { value: "classic", label: "Classic (validation)" },
            ]}
            onChange={(value) => {
              if (value === "workbench" || value === "classic") setPresentation(value);
            }}
            description="Choose before connecting. Both views use the same commands and access checks."
          />
          <Button type="submit" variant="primary">
            Open development scope
          </Button>
        </form>
      </section>
    </main>
  );
}
