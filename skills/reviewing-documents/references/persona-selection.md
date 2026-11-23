# Selecting the review team

The team is `coherence-reviewer` and `feasibility-reviewer` always, plus each conditional lens whose signals the document shows. Where a signal below names a requirements document, it holds for a `spec` too; where it names a plan, it holds for an `other` document that prescribes steps someone carries out.

## Activation signals

| Lens | Activate when the document shows |
| --- | --- |
| `product-lens-reviewer` | A product position a knowledgeable stakeholder could reasonably challenge and that its origin did not settle, or strategic weight beyond the immediate problem (both below) |
| `design-lens-reviewer` | UI or UX references, frontend components or visual design language; user flows, wireframes, screens, pages or views; interactions (forms, buttons, navigation, modals); responsive behavior or accessibility |
| `security-lens-reviewer` | Authentication or authorization, login flows, session management; API endpoints exposed to outside clients; handling of sensitive data (personal data, payments, tokens, credentials, secrets, encryption); third-party integrations that cross a trust boundary |
| `scope-guardian-reviewer` | Every plan, since its mechanism-sizing check applies to all plans. Any other document with priority tiers (P0/P1/P2, must/should/nice-to-have); more than 8 distinct requirements or units; stretch goals, nice-to-haves or a future-work section; scope language misaligned with the stated goals; goals that connect to no requirement |
| `adversarial-document-reviewer` | A high-value challenge surface (below), not mere structural complexity |

### product-lens: either leg qualifies

Users may be end users, developers, operators, maintainers or any other audience.

- **Unsettled product position:** a problem framing, a goal predicting a specific user outcome, or a ranking of what gets built against what is deferred, which the origin did not settle. A choice among mechanisms for an agreed outcome is an implementation decision, not a product position. Describing a task or restating known requirements stakes nothing.
- **Strategic weight:** the work could shape what the system becomes known for; it is a complexity or simplicity bet affecting adoption, onboarding or cognitive load; it opens or closes future directions (path dependencies, architectural commitments); or it carries an opportunity cost.

### security-lens: the sensitive-data bound

- **Ordinary data handling is no trigger.** Neither is storage churn on its own: an internal schema migration, a field rename or a data-store move activates the lens only when the data is sensitive or the change alters who can read or write it.
- **Deployment-ordering risk is feasibility's,** not a security signal.

### adversarial: the challenge-surface bar

Activate when any of these holds:

- A requirements document with 2 or more challengeable claims: problem framing, solution selection, prioritization, predicted outcomes.
- A high-stakes domain, whatever the type or size: authentication, payments, billing, data migrations, privacy or compliance, external integrations, cryptography.
- A new abstraction, framework or significant architectural pattern.
- A plan whose `{origin}` is `none`: its premise was never validated upstream.
- A plan that extends scope beyond its origin: new actors, new flows, deferred features restored.
- An explicit alternatives section, or unresolved tradeoffs.

Do not activate it on a routine plan that derives from an approved origin, stays in its scope, and brings no high-stakes domain or new abstraction. A well-structured plan with stated reasons is the plan doing its job: activating on that alone reopens settled questions.

## Announce the team

Before any dispatch, tell the user (or put in the result) which lenses review and why, one reason for each conditional lens:

```
Reviewing with:
- coherence-reviewer (always-on)
- feasibility-reviewer (always-on)
- scope-guardian-reviewer -- plan has 12 requirements across 3 priority levels
- security-lens-reviewer -- plan adds API endpoints with an auth flow
```
