# Rendering a finding for a reader

These rules hold wherever a finding is shown: the non-interactive result, the interactive presentation, the walk-through block and question, the bulk preview, and an Open Questions entry. Each keeps its own layout and maps it onto these rules.

The reader does not have the document open and has not learned its identifiers or the project's code. The output exists so they can decide Apply, Defer or Skip without rebuilding the finding from expert narrative. A finding whose only path to a decision is "go read the code" fails, however correct it is.

Keep the exact identifiers and domain terms that help the decision, and every distinct consequence, qualification and required action. Write each line as `skill://plain-words` says; the rules below add to it.

## Decision-first order

For each finding the reader is asked about (a decision, or a member of a grouped confirmation):

1. **Recommendation:** Apply, Defer or Skip, from synthesis, stated up front.
2. **Consequence if unchanged:** one short sentence per distinct consequence: what goes wrong, for whom. It holds no identifier at all: a reader who skimmed the document once can judge it.
3. **Change:** one sentence of intent: what the fix achieves and where it lives, not quoted text or markup.
4. **Basis:** at most two sentences of mechanism, each identifier glossed, at most two identifiers in the whole block.
5. **Trace on request:** nothing beyond that is printed. Offer it in one closing line ("Ask for the call-path detail.").

A layout satisfies this when the consequence reads first with no identifier, and the recommendation is unmistakable; it need not print these labels.

## Reporting, proposing, asking

The wording says which it is, even with the section heading off screen:

- **A report** is a change already applied: say what changed, with no recommendation and no options. Never phrase it as a question.
- **A grouped confirmation** applies on one answer: render every member in full before the question.
- **A question** needs the reader's judgment: it carries its options and names what differs between them.
- **The summary line counts separately** the changes made, the proposed fixes awaiting approval and the questions needing judgment. Never imply that a proposed fix has landed.

## Presenting a batch

- **Open with what the batch does,** in one or two sentences: "Six fixes update the setup steps to match the renamed commands", not "6 proposed fixes".
- **Group members by what they share,** usually the change (one cause, one kind of edit, one section), and head each group with the shared consequence. Severity and lens only order members inside a group.
- **Show every member.** Grouping reorganizes; it never hides.
- **Invent no structure.** Findings that share nothing stand alone: an invented group asserts a relationship the reader will act on.

## Obligations

An obligation (a correction the document already entails, with a written fix) carries no recommendation. Render it as one line, the consequence and then the change as intent, under the unit or section it affects; name that unit or section once, as the group heading. An Open Questions entry, a residual concern, a deferred question and an FYI observation are single lines too.

## Identifiers the reader cannot resolve

Classify each identifier by what it does, never by a product's own vocabulary:

| Kind | Example | Render |
| --- | --- | --- |
| Defined by the document | `R6`, `U3`, `KTD2`, `AE1` | Keep the id, with a short handle at first mention: `R6 (suppress peer panels on low-stakes calls)`. Later mentions in the block stay bare |
| An event outside the document | a ticket key, a pull request number, an incident | Name it, with its role, only when that event changes the decision; otherwise move it to the trace |
| A code symbol | a function, file, variable or line reference | Say the role it plays ("the check for a permanent failure"); keep the symbol only when the decision turns on its exact scope |

- **At most two such identifiers per finding block;** the rest go to the trace. Plain section names ("Requirements") need no handle.
- **Never emit a bare identifier.** The lens wrote each handle into its finding; render it. If one is missing, look it up in the document, and treat the gap as a defect of the lens's output.
- **Resolve a handle again** only when an Apply renumbered or renamed the item it names.

## Words and spans

- **Use the reader's words for the routes:** "applied changes" or "fixes", "proposed fixes", "decisions", "FYI observations". The internal classes (`safe_auto`, `gated_auto`, `manual`) appear only in a table's Tier column.
- **At most 2 inline code spans per sentence,** each one identifier, flag or short phrase, with a space on each side.
- **No diff blocks:** describe a change to the document in prose. A raw code block only for new content of 5 lines or fewer that replaces nothing.
- **Pipe tables only** (`| a | b |`), never box-drawing characters. Escape a literal `|` inside a cell as `\|`.
