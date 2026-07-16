# Curated implementation references

The full upstream reference baseline remains in the DataGround specification. This file adds the sources most relevant to repository bootstrap, Codex handoff and the design system.

## DataGround authority

- DataGround system specification, Draft 0.4.1
- DataGround architecture decision register, ADR-001–038

The repository copies are [`../architecture/system-specification.md`](../architecture/system-specification.md) and [`../architecture/decision-register.md`](../architecture/decision-register.md).

## Codex implementation workflow

- [Codex best practices](https://learn.chatgpt.com/guides/best-practices)
- [Codex prompting](https://learn.chatgpt.com/docs/prompting)
- [AGENTS.md guidance](https://learn.chatgpt.com/docs/agent-configuration/agents-md)
- [Codex execution plans](https://developers.openai.com/cookbook/articles/codex_exec_plans)

The handoff prompts follow the documented goal, context, constraints and done-when pattern. Durable repository instructions belong in a short, verified `AGENTS.md`; complex tasks use an execution plan and explicit verification.

## Design system and accessibility

- [WCAG 2.2](https://www.w3.org/TR/WCAG22/)
- [WAI-ARIA Authoring Practices Guide](https://www.w3.org/WAI/ARIA/apg/)
- [Design Tokens Format Module](https://www.designtokens.org/tr/drafts/format/)
- [React Aria quality and accessibility](https://react-aria.adobe.com/quality#accessibility)
- [Storybook accessibility testing](https://storybook.js.org/docs/writing-tests/accessibility-testing)

WCAG 2.2 is a W3C Recommendation and the release target is AA. The Design Tokens Format Module is a community-group specification rather than a W3C Standard, so its revision must be pinned. React Aria is recommended only if frontend discovery confirms React. Automated Storybook checks are a first line of QA and do not replace manual accessibility testing.

## Contract and controller foundations

- [OpenAPI Specification 3.1.2](https://spec.openapis.org/oas/v3.1.2.html)
- [JSON Schema 2020-12](https://json-schema.org/draft/2020-12)
- [Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html)
- [Kubernetes controller pattern](https://kubernetes.io/docs/concepts/architecture/controller/)
- [OpenTelemetry specification](https://opentelemetry.io/docs/specs/otel/)

## Runtime and sandbox references

Use the exact source list in Section 25 of the specification. At implementation time, snapshot or record the pages used for each certified version and preserve generated schemas/fixtures in the release evidence. Mutable documentation pages do not define production support.
