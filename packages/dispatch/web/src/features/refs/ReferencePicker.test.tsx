import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: the Enter that ends an IME composition is a key marked as the input method's, which user-event has no form for.
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { api } from "../../api/client";
import { issue } from "../margin/margin-fixture";
import { ReferencePicker } from "./ReferencePicker";

// The picker's filter picks its top match at Enter. Safari sends the Enter that ends an input
// method's composition after the composition's end, unmarked - `isComposing` false - with keyCode
// 229 (WebKit bug 165004): that Enter commits what the reader composed into the filter and is the
// input method's, so it picks nothing. The plain Enter after it picks the top match.
test("an Enter that ends an IME composition in Safari, at keyCode 229, picks nothing, and a plain Enter picks the top match", async () => {
  const user = userEvent.setup();
  const getIssue = spyOn(api, "getIssue").mockResolvedValue(issue);
  const listIssues = spyOn(api, "listIssues").mockResolvedValue([
    { ...issue, key: "CORE-2", open_asks: 0, title: "Related work" },
  ]);
  const picked: string[] = [];
  const view = render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <ReferencePicker
        issueKey={issue.key}
        onClose={() => {}}
        onSelect={(reference) => picked.push(reference)}
      />
    </QueryClientProvider>
  );
  try {
    await screen.findByRole("button", { name: "CORE-2: Related work" });
    const filter = screen.getByLabelText("Filter issues");
    await user.click(filter);
    // user-event has no form for this: it cannot mark a key as an IME composition.
    fireEvent.keyDown(filter, { isComposing: false, key: "Enter", keyCode: 229 });
    expect(picked, "Enter at keyCode 229").toEqual([]);

    await user.keyboard("{Enter}");
    expect(picked).toEqual(["dispatch://CORE-2"]);
  } finally {
    view.unmount();
    getIssue.mockRestore();
    listIssues.mockRestore();
  }
});
