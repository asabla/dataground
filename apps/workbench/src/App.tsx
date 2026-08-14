import { Button, StatusBadge, TextField } from "@dataground/ui";
import { useState } from "react";
import { createDataGroundClient, type DataGroundClient } from "./contracts/client";
import type { ServiceRevisionResource } from "./revisions";
import { AgentServiceAuthoringWorkflow, type AgentServiceResource } from "./services";

const DEFAULT_ISOLATION_DOMAIN_ID = "iso_00000000000000000001";
const ISOLATION_DOMAIN_ID_PATTERN = /^iso_[0-9a-z]{20,32}$/u;
const BEARER_TOKEN_PATTERN = /^[A-Za-z0-9\-._~+/]+=*$/u;

interface DevelopmentScopeErrors {
  bearerToken?: string;
  isolationDomainId?: string;
}

interface DevelopmentSession {
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
  const [bearerToken, setBearerToken] = useState("");
  const [isolationDomainId, setIsolationDomainId] = useState(DEFAULT_ISOLATION_DOMAIN_ID);
  const [openedRevision, setOpenedRevision] = useState<ServiceRevisionResource>();
  const [openedService, setOpenedService] = useState<AgentServiceResource>();
  const [session, setSession] = useState<DevelopmentSession>();
  const [validationErrors, setValidationErrors] = useState<DevelopmentScopeErrors>({});

  return (
    <main className="shell">
      <header className="masthead">
        <p className="eyebrow">Self-hosted data and agent platform</p>
        <h1>DataGround</h1>
      </header>

      {session ? (
        <>
          <section className="development-session" aria-labelledby="development-session-title">
            <div className="development-session__heading">
              <div>
                <StatusBadge tone="active">Development scope active</StatusBadge>
                <h2 id="development-session-title">Reference API session</h2>
              </div>
              <Button
                onPress={() => {
                  setSession(undefined);
                  setBearerToken("");
                  setOpenedRevision(undefined);
                  setOpenedService(undefined);
                }}
                variant="quiet"
              >
                Disconnect
              </Button>
            </div>
            <dl className="development-session__scope">
              <div>
                <dt>Isolation domain</dt>
                <dd>{session.isolationDomainId}</dd>
              </div>
            </dl>
            <p>
              The API independently authenticates and authorizes every command. Disconnecting clears
              the in-memory client but does not cancel commands already accepted by the API.
            </p>
          </section>

          <section className="product-workflow" aria-label="Agent service workflow">
            <AgentServiceAuthoringWorkflow
              canCreateRevision
              canCreateService
              canPublishRevision
              client={session.client}
              isolationDomainId={session.isolationDomainId}
              onOpenRevision={setOpenedRevision}
              onOpenService={(service) => {
                setOpenedRevision(undefined);
                setOpenedService(service);
              }}
              selectedRevision={openedRevision}
              selectedService={openedService}
            />
          </section>
        </>
      ) : (
        <section className="development-connection" aria-labelledby="development-connection-title">
          <div>
            <StatusBadge tone="warning">Development only</StatusBadge>
            <h2 id="development-connection-title">Connect to the reference API</h2>
            <p>
              Open one explicit isolation scope before issuing governed commands. The Workbench
              sends requests through its same-origin development proxy and keeps the bearer token
              only in component memory.
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
            <Button type="submit" variant="primary">
              Open development scope
            </Button>
          </form>
        </section>
      )}
    </main>
  );
}
