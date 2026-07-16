# DataGround contracts

`openapi/dataground-api.openapi.json` is the canonical public HTTP contract. JSON Schema
2020-12 documents under `schemas/` are canonical for non-HTTP manifests. Generated workbench
types derive from OpenAPI and must never be edited directly.

Run `pnpm contracts:generate` after an accepted OpenAPI change. `pnpm contracts:check` validates
the structural rules, valid and invalid fixtures, isolation-scoped resource paths, idempotency
headers on mutations, and the frozen v1 compatibility baseline. `pnpm
contracts:generate:check` rejects stale generated types.

Within v1, adding optional object fields and new operations is compatible. Removing or renaming
an operation, removing a published property, adding a required property, narrowing accepted
values, changing state or error semantics, or changing an identifier or event-envelope invariant
is breaking. Unknown response fields and event types must remain ignorable. A proposed breaking
change requires a new major API path and an explicit migration decision; do not update the
baseline merely to make a check pass.

The reference API is intentionally in-memory. It proves the contract and deterministic runtime
semantics but does not satisfy durability, authorization, audit, or restart-recovery gates. Those
boundaries begin in prompt `02`.
