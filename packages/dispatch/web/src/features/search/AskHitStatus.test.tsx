import { afterEach, expect, test } from "bun:test";
import { cleanup } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { renderWithQueries as render } from "../../__tests__/query-provider";
import type { SearchAsk } from "../../api/types";
import { AskHitStatus } from "./AskHitStatus";

afterEach(cleanup);

// A hit carries its answer's chosen options as their labels alone (`SearchAsk`), with no tree
// and no option to take one from, and the page parses no stored text: `Chose …` shows each label
// as written (`search-ask-chosen-options`). Drawn by the renderer, the label would be parsed in
// the page and read `Chose Brass it`.
test("an ask hit's chosen options are their labels as written, parsed by nothing", () => {
  const ask = {
    answer: {
      at: "2026-10-08T03:00:00Z",
      selected: ["**Brass** it", "Steel"],
      text: null,
      user: "alice",
    },
    state: "answered",
  } as unknown as SearchAsk;
  const view = render(
    <MemoryRouter>
      <AskHitStatus ask={ask} muted={false} />
    </MemoryRouter>
  );
  try {
    expect(view.container.textContent).toContain("Chose **Brass** it, Steel");
    expect(view.container.querySelector("strong")).toBeNull();
  } finally {
    view.unmount();
  }
});
