import { expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { http } from "msw";
import type { ReactNode } from "react";

import { MEANING_PAUSE_MS, useTieredSearch } from "../features/search/useTieredSearch";
import { answerRequests } from "./network";

// Both tiers ask `/api/v2/search-results`, the one search route. The keystroke's tier asks for the
// keyword leg alone (`useTieredSearch`, `keywords_only=true`), which makes no embedding call
// behind each keystroke; the pause's tier sends the request as the chips built it, so a reader
// who narrowed by nothing searches by meaning too wherever an embedder is configured.
test("the keystroke tier asks for the keyword leg alone, and the pause tier for the whole search", async () => {
  // What the two tiers asked for, in the order they were asked. Each is answered an empty page,
  // which is all the hook needs to resolve.
  const asked: string[] = [];
  answerRequests(
    http.get("/api/v2/search-results", ({ request }) => {
      const url = new URL(request.url);
      asked.push(`${url.pathname}${url.search}`);
      return Response.json({
        filters: {},
        items: [],
        limit: 50,
        offset: 0,
        query: {},
        reachable: 0,
        took_ms: 1,
        total: 0,
      });
    })
  );

  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }): ReactNode => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  const hook = renderHook(
    () => useTieredSearch({ enabled: true, request: {}, words: "astrolabe" }),
    { wrapper }
  );

  try {
    await waitFor(
      () =>
        expect(asked).toEqual([
          "/api/v2/search-results?q=astrolabe&keywords_only=true",
          "/api/v2/search-results?q=astrolabe",
        ]),
      {
        // Both tiers' waits, and the answers behind them.
        timeout: MEANING_PAUSE_MS * 8,
      }
    );
  } finally {
    hook.unmount();
    client.clear();
  }
});
