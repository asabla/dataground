import assert from "node:assert/strict";
import { describe, it } from "vitest";
import type { ServiceAliasResource } from "../aliases";
import type { PublishedServiceRevisionResource, ServiceRevisionResource } from "../revisions";
import type { AgentServiceResource } from "./client";
import {
  isAliasSelectedForPublishedRevision,
  isPublishedRevisionSelectedForService,
  isRevisionSelectedForService,
  isServiceSelectedForScope,
} from "./selection";

const isolationDomainId = "iso_00000000000000000001";
const service: AgentServiceResource = {
  metadata: {
    createdAt: "2026-08-15T00:00:00Z",
    createdBy: "usr_00000000000000000001",
    generation: 1,
    id: "svc_00000000000000000001",
    isolationDomainId,
    updatedAt: "2026-08-15T00:00:00Z",
    version: 1,
  },
  name: "Research agent",
};
const revision: ServiceRevisionResource = {
  inputSchema: {
    additionalProperties: false,
    properties: { prompt: { maxLength: 262_144, minLength: 1, type: "string" } },
    required: ["prompt"],
    type: "object",
  },
  metadata: {
    createdAt: "2026-08-15T00:01:00Z",
    createdBy: "usr_00000000000000000001",
    generation: 1,
    id: "rev_00000000000000000001",
    isolationDomainId,
    updatedAt: "2026-08-15T00:01:00Z",
    version: 1,
  },
  requiredCapabilities: ["tool"],
  revisionNumber: 1,
  runtimeProfile: "reference/v1",
  serviceId: service.metadata.id,
  state: "draft",
};
const publishedRevision: PublishedServiceRevisionResource = {
  ...revision,
  metadata: {
    ...revision.metadata,
    updatedAt: "2026-08-15T00:02:00Z",
    version: 2,
  },
  publishedAt: "2026-08-15T00:02:00Z",
  state: "published",
};
const alias: ServiceAliasResource = {
  metadata: {
    createdAt: "2026-08-15T00:03:00Z",
    createdBy: "usr_00000000000000000001",
    generation: 1,
    id: "als_00000000000000000001",
    isolationDomainId,
    updatedAt: "2026-08-15T00:03:00Z",
    version: 1,
  },
  name: "stable",
  revisionId: publishedRevision.metadata.id,
  serviceId: service.metadata.id,
};

describe("retained service command selection", () => {
  it("accepts only the exact service, draft, publication and route relationship", () => {
    assert.equal(isServiceSelectedForScope(service, isolationDomainId), true);
    assert.equal(isRevisionSelectedForService(revision, service, isolationDomainId), true);
    assert.equal(
      isPublishedRevisionSelectedForService(
        publishedRevision,
        revision,
        service,
        isolationDomainId,
      ),
      true,
    );
    assert.equal(
      isAliasSelectedForPublishedRevision(
        alias,
        publishedRevision,
        revision,
        service,
        isolationDomainId,
      ),
      true,
    );
  });
  it("rejects cross-scope and malformed service identities", () => {
    for (const changed of [
      { ...service, metadata: { ...service.metadata, id: "invalid" } },
      {
        ...service,
        metadata: { ...service.metadata, isolationDomainId: "iso_00000000000000000002" },
      },
    ])
      assert.equal(isServiceSelectedForScope(changed, isolationDomainId), false);
    assert.equal(
      isServiceSelectedForScope(
        { ...service, metadata: { ...service.metadata, isolationDomainId: "invalid" } },
        "invalid",
      ),
      false,
    );
  });
  it("rejects a substituted or malformed revision before publication", () => {
    for (const changed of [
      { ...revision, serviceId: "svc_00000000000000000002" },
      {
        ...revision,
        metadata: { ...revision.metadata, isolationDomainId: "iso_00000000000000000002" },
      },
      { ...revision, runtimeProfile: " reference/v1" },
    ])
      assert.equal(isRevisionSelectedForService(changed, service, isolationDomainId), false);
  });
  it("rejects a publication with a different service, scope or definition", () => {
    for (const changed of [
      { ...publishedRevision, serviceId: "svc_00000000000000000002" },
      {
        ...publishedRevision,
        metadata: { ...publishedRevision.metadata, isolationDomainId: "iso_00000000000000000002" },
      },
      { ...publishedRevision, runtimeProfile: "other/v1" },
    ])
      assert.equal(
        isPublishedRevisionSelectedForService(changed, revision, service, isolationDomainId),
        false,
      );
  });
  it("rejects a stale, cross-scope or substituted route before composition", () => {
    for (const changed of [
      { ...alias, serviceId: "svc_00000000000000000002" },
      { ...alias, revisionId: "rev_00000000000000000002" },
      { ...alias, metadata: { ...alias.metadata, isolationDomainId: "iso_00000000000000000002" } },
      {
        ...alias,
        metadata: {
          ...alias.metadata,
          createdAt: "2026-08-15T00:01:30Z",
          updatedAt: "2026-08-15T00:01:30Z",
        },
      },
    ])
      assert.equal(
        isAliasSelectedForPublishedRevision(
          changed,
          publishedRevision,
          revision,
          service,
          isolationDomainId,
        ),
        false,
      );
  });
});
