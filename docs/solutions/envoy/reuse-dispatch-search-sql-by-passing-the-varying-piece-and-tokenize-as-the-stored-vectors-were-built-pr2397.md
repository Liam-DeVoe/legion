---
title: "Reuse Dispatch's search SQL by passing the varying piece in, and tokenize ad-hoc text the way the stored vectors were built"
category: envoy
tags:
  - dispatch
  - search
  - sql
  - tsvector
date: 2026-10-28
status: active
module: packages/envoy/internal/dispatch/api
---

# Reuse Dispatch's search SQL by passing the varying piece in, and tokenize ad-hoc text the way the stored vectors were built

- To run Dispatch's search with a different rank, or a different `q`, build on the functions in
  `packages/envoy/internal/dispatch/api/search.go`: `searchKeywordLegs(rank)` takes the rank
  expression for each keyword arm, `searchQueryBody(legs)` and `searchQueryMeaningBody(legs)` take
  the legs, and `searchQOpener` is search's own `q`. Prefix the body with your own `q` and pass
  your rank. Do not copy the SQL, and do not rewrite the generated string with substitutions: a
  later edit of search then breaks your copy silently or at package init.
- When you change these functions, prove live search's query text is unchanged byte for byte: dump
  `searchQuery` and `searchQueryMeaning` before and after with a throwaway test and compare.
- SQL that tokenizes ad-hoc text to compare with stored `search` vectors builds its text with the
  stored vector's own function. For a document version that is `search_document_text`, which
  removes the ask blocks and then applies `search_text`, then `search_vector`
  (`store/migrations/20261121060636_search_document_text.up.sql`). A copy of the pipeline drifts:
  until that migration the triggers applied `search_text` first, and removing the blocks after it
  joined underscore runs into one run the parser read in quadratic time.

## Evidence

sjawhar/legion#2397's related-item check first rewrote five `ts_rank_cd` expressions inside copies
of search's SQL at package init, behind a panic guard; review moved it to
`searchKeywordLegs(relatedCoordinationRank)` (110627e7, `suggestions.go:78-86`), and the four
generated queries were compared byte for byte (5,611 / 10,735 / 6,104 / 11,229 bytes). Its picker
stripped ask blocks before `search_text` and picked nothing for a lexeme only that order produces;
1289c778 matched 0087's order (`related_terms.go`), pinned by
`TestRelatedTermsTokenizeATextAsADocumentVersionsVectorIsBuilt`.
cybermonklabs/legion's strip-first change replaced that order in the triggers and in the picker
with `search_document_text`, after a review measured the version insert of 41,000 bytes of `a_`
runs between ask blocks at 12 s; `TestSearchHandsTheParserNoRunJoinedAcrossAnAskBlock` reads the
text each path hands the parser.
