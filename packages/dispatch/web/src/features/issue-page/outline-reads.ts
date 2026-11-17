import { queryOptions } from "@tanstack/react-query";

import { api } from "../../api/client";

// The reads the outline draws the conversation from - the issue's asks, its comments and its
// messages - under the keys and by the requests `useIssueTree.ts` makes them with. The box
// (`IssueComposer.tsx`) and the read mark (`IssueReadMark.tsx`) observe them, and take each one
// from here rather than writing it out again: a key holds whatever the first request mounted under
// it asked for, so a copy whose request had drifted from the outline's would put another answer
// under the same key, and nothing would fail to say so. The outline (`useIssueTree.ts`) and its
// writer (`outline-writer.tsx`) still write the three reads out themselves. Once the outline's own
// module exports its asks read, that export takes the place of `outlineAsksQuery`.

/** Every ask of the issue, whatever its state (`GET /api/v1/issues/{key}/asks?state=all`). */
export const outlineAsksQuery = (key: string) =>
  queryOptions({
    queryKey: ["asks", key],
    queryFn: () => api.listIssueAsks(key, "all"),
  });

/** The issue's comments (`GET /api/v1/issues/{key}/comments`). */
export const outlineCommentsQuery = (key: string) =>
  queryOptions({
    queryKey: ["comments", key],
    queryFn: () => api.listComments(key),
  });

/** The issue's messages, each with its replies (`GET /api/v1/issues/{key}/messages`). */
export const outlineMessagesQuery = (key: string) =>
  queryOptions({
    queryKey: ["issue-messages", key],
    queryFn: () => api.listIssueMessages(key),
  });
