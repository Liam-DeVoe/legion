# Getting the document, and classifying it

## Get the document

| What you have | Do |
| --- | --- |
| A path | Read it. If the read fails or the file is not on disk, give the not-found message below |
| A Dispatch document (a spec, an artifact) | Read it with `dispatch doc-read --ref <dispatch:// reference>`, or `--issue <KEY>` for an issue's spec, and save it to a scratch file: that file is the path the lenses read. Edit the document itself only through `dispatch`, as `skill://dispatch` says. Where `dispatch` is not on `PATH` or the read fails, dispatch no lens: ask for a readable path (interactive), or return the not-found message below (non-interactive) |
| The document's text, pasted into the request | Save it to a scratch file of your own (`mktemp -d`): that file is the path the lenses read |
| Nothing, interactive | Ask which document to review, or offer the newest plan or spec this conversation or the repository holds |
| Nothing, non-interactive | Return the failure line `references/modes.md` gives, and dispatch no lens |

- **A copy carries no edit authority.** When you hold the document only as a scratch copy (pasted text, a download you may not write back), apply nothing: each correction routed to Apply is returned as a proposed fix instead.
- **Read the sources the document names** that the agreed work depends on: the code, the issue, the approved upstream document. Synthesis checks each claim against them.

## When a document cannot be read

SKILL.md step 2 holds the rule: verify every path before any dispatch. When a path is unreadable, give the message for your mode, and dispatch no lens:

- **Interactive:** stop and say: `Document(s) not found on disk: <paths>. Check out the branch containing them, use a worktree, or provide corrected readable paths before retrying the review.`
- **Non-interactive:** return `Review failed: document(s) not found on disk: <paths>. Expected input: paths to readable files on disk; check out the branch containing them or provide corrected paths.`

## Resume a completed review

- **Reuse a complete earlier review** of the same document and scope (its lens returns, evidence, classification and decisions) when the document and its sources have not materially changed. Check the current document against the saved state: a summary alone is not enough.
- **A new mode is not a new review**, and neither is a request to handle the existing findings.
- **A fresh review takes the normal dispatch path:** an explicit request for one, earlier evidence that is incomplete, or a material change in the document or its sources. Keep the earlier decisions as history either way.
- **On a valid resume, go straight to synthesis** with the retained state. Dispatch no team again.
- **One fresh lens may corroborate** a retained, worthwhile correction when missing corroboration alone blocks it. Use the normal brief from `references/dispatch.md` and a fresh context. Give it the document, its sources, the agreed outcome and the constraints; never the earlier claims, fixes or questions.
- **Reconcile new answers** from the author with the retained state before you route anything.

## Classify the document

Each type's signals:

| Type | Signals |
| --- | --- |
| `requirements` | What to build: actors, flows, scope boundaries, success criteria, open questions, `R1`, `A1`, `F1` or `AE1` identifiers; no implementation units and no per-unit file lists |
| `spec` | Requirements stated precisely enough to test: numbered requirements with acceptance checks or a verification section, Dispatch `:::requirement` or `check` blocks, ask blocks |
| `plan` | How to build: implementation units or tasks (`U1`, Task 1), each with its files, approach and tests; repository paths to create or change; sequencing; key technical decisions |
| `other` | Anything else a reader acts on: a design note, a runbook or procedure, a pull request body, a report |

- **The dominant shape wins.** Any implementation planning makes the document a `plan`, including planning that is incomplete or blocked: review those sections too.
- **Ambiguous between `requirements` and `plan`:** choose `requirements`. It activates fewer plan-only feasibility checks.
- **Pass the type to each lens** in the `{document_type}` slot.

## Extract once, for every lens

Lenses never parse the document for these two. Fill both even when the value is `none`: an empty slot silently switches off a lens's restraint on settled premises.

- **`{origin}`:** the upstream document whose approval settled this one's premise (a plan's approved spec, a spec's parent requirements), by path or `dispatch://` reference; else `none`.
- **`{settled_decisions}`:** each decision the document or its origin records as already made by the author or the person they answer to (an answered ask, a decision record, a `session-settled:` note), with its rejected alternative; else `none`.
