---
name: update-agents-md
description: Review, create, or update repository AGENTS.md guidance while keeping it accurate, concise, durable, and compatible with Codex and OpenCode. Use when agent instructions are missing or stale, repository architecture or commands change, recurring agent mistakes reveal a guidance gap, or a user asks to improve AGENTS.md.
---

# Update AGENTS.md

Maintain agent guidance as an accurate operating contract for DataGround. Prefer the smallest change that resolves a durable guidance gap.

## Establish scope and evidence

Read the root `AGENTS.md` and any nested `AGENTS.md` files that govern the affected area. Inspect the repository tree, build manifests, CI configuration, README, architecture and decision records, contribution guidance, and commands used by the current toolchain. Review relevant recent changes when they explain why guidance has become stale.

Treat root guidance as repository-wide and nested guidance as an override for its subtree. Before adding a nested file, confirm that the local rule genuinely differs; do not repeat root instructions merely to make them visible nearby.

Distinguish verified repository facts from proposed future state. Do not add a command, path, capability, dependency, or guarantee unless it exists and has been checked. When the repository is still an early scaffold, state its limitations plainly.

Classify proposed instructions before editing. Durable repository conventions, architecture boundaries, verification commands, safety invariants, and completion criteria belong in `AGENTS.md`. Keep one-off task requests and personal preferences out unless the repository has explicitly adopted them as team policy. Prefer tests, linters, CI, schemas, or hooks for mechanically enforceable rules. Put detailed repeatable procedures in a focused skill or document and link to them rather than growing root guidance indefinitely.

## Preserve DataGround boundaries

Do not weaken the product boundaries, fail-closed authorization, credential mediation, isolation scoping, durable reconciliation, compatibility, or verification requirements already established in root guidance and confirmed architecture decisions.

If a proposed instruction changes public contracts, trust boundaries, runtime integration, storage semantics, workflow ownership, tenancy, or release guarantees, verify that a confirmed decision supports it. Otherwise stop and propose a decision rather than embedding new architecture in agent guidance.

## Edit for supported harnesses

Keep repository skills in `.agents/skills`, the shared discovery location used by Codex and OpenCode. Do not duplicate the same skill under `.opencode`, `.codex`, or another tool-specific directory.

Use standard Markdown and repository-relative paths. Keep shared instructions tool-neutral so different coding harnesses receive the same guidance. Do not require a harness-specific command, invocation syntax, configuration file, or tool unless the repository depends on it; isolate unavoidable tool-specific guidance and label it clearly.

Write concise prose by default. Use lists for real sequences, checklists, or exact sets, not as the default shape for every explanation. Preserve useful terminology and instructions. Remove duplication, vague advice, stale commands, and generic statements that do not change agent behavior.

Link to canonical architecture, contribution, security, or operations documentation when detail already lives there. Summarize only what an agent must know before acting. Do not copy volatile dependency details, release versions, capacity numbers, or implementation inventories into `AGENTS.md`.

Do not add time estimates. When instructions conflict, resolve the conflict in favor of the closest applicable `AGENTS.md` and make the scope explicit.

## Review in five passes

For a substantive change, review the complete candidate five times and retain only improvements.

1. Verify factual accuracy against the current tree, documentation, and runnable commands.
2. Check scope and precedence, including nested guidance and linked documents.
3. Check alignment with maintainable architecture and durable solutions without prescribing speculative design.
4. Check security, isolation, compatibility, recovery, and definition-of-done requirements for weakening or ambiguity.
5. Remove repetition, unnecessary lists, harness-specific assumptions, and wording likely to become stale.

Keep concise working notes while editing so regressions remain visible. Do not carry iteration scores or review notes into repository guidance, commits, or pull request descriptions. A pass that finds no genuine improvement leaves the file unchanged.

## Validate and hand off

Read the final file from top to bottom as a new agent would. Verify every referenced path and run safe documented commands when the change depends on them. Check that this skill remains aligned if the maintenance procedure itself changed.

Summarize the durable outcome in the final handoff. For pull requests, follow root PR-writing guidance: focus on why, omit diff narration and procedural review notes, and leave routine verification to configured checks. Do not claim guidance is enforced unless a test, CI check, hook, schema, or repository rule provides that enforcement.
