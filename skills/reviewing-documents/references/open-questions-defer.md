# Deferring a finding to the document's Open Questions

Interactive mode only. A Defer, from the walk-through or the bulk preview, appends the finding to a visible `Deferred / Open Questions` section of the document, in the document's own format. Never insert Markdown into HTML.

## Append

1. **Find or create the section.** Match an existing `Deferred / Open Questions` heading exactly, and append inside it where it stands. With none, create `## Deferred / Open Questions` near the end of the document, before any trailing footer; in HTML, follow the document's own section and heading pattern.
2. **Find or create today's subsection,** `### From YYYY-MM-DD review` with the ISO date, as the section's last subsection. Defers on the same day share it.
3. **Re-read the document from disk** just before each write. If it changed since your last read, do not write: take the failure path below. The user may be editing it.
4. **Append the entry,** in HTML mirroring the nearest sibling entry, else one list item per finding:

```
- **{title}** — {section} ({severity}, {reviewer}, confidence {confidence})

  {why_it_matters}
```

- **Fields:** the title; the section as the finding names it; P0 to P3; every lens that raised it; the anchor as an integer, 50, 75 or 100; the full consequence text.
- **Render the title and consequence under `references/rendering-floor.md`:** a later reader has none of the review's context. The entry is a concern, so it carries no recommendation, change or basis.
- **Leave out the suggested fix and the evidence.**

## Never append twice

Before you write, compute a key from the rendered entry you are about to write: its normalized section and title (lowercase, punctuation stripped, whitespace collapsed) plus the first 120 or so characters of its rendered consequence, cut on a word boundary. Compute the same from each entry already under today's subsection.

- **A match is a no-op.** Record it in the completion report's Coverage.
- **Entries under other dates never match:** each review may raise a concern again.
- **An old entry's `<!-- dedup-key: ... -->` comment** is ignored for matching; strip it if you edit that entry, and write no new one.

## When the append fails

A read-only file, a bad path, an edit error or a concurrent edit: report it at once with the question tool.

Stem: `Couldn't append the finding to Open Questions. What should the agent do?`

```
A. Retry the append
B. Record the deferral in the completion report only (don't mutate the document)
C. Convert this finding to Skip
```

- **A:** try again; on another failure, ask again.
- **B:** record it Deferred, with a note that the append failed.
- **C:** record it Skipped, with the reason `append to Open Questions failed: <error>`.
- **No answer** (the session ended): take B.
- **Never fail silently.**

## Availability

When the document is known to be unwritable at the start of presentation, cache `append_available: false`: the routing question drops option C, the walk-through drops Defer, and the preview downgrades Defer to Skip. A single failed append mid-flow takes the failure path and leaves that cache as it is.
