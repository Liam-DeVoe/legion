import { expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import type { Agent, IssueReferenceSummary, MyReplyRow } from "../../api/types";
import { ThreadTitle } from "../inbox/ThreadTitle";
import { MarkdownBody } from "./MarkdownBody";
import { PlainText } from "./plain-text";
import { ReferenceHostIs } from "./reference-host";

/**
 * The one naming rule and the four host shapes, read on a painted body and on the plain-text
 * projection beside it. Every row here names CORE-1, whose summary the cache already holds, so
 * what is being read is how the rule draws a resolved reference - never whether it resolves.
 */

const summary: IssueReferenceSummary = {
  documents: [],
  key: "CORE-1",
  project: "CORE",
  status: "in_progress",
  title: "Fix the thing",
};

/** A query client holding CORE-1's summary, so every reference below resolves at once. */
function clientWithIssue(): QueryClient {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["issue-summary", "CORE-1"], summary);
  return client;
}

function draw(children: ReactNode, client: QueryClient = clientWithIssue()) {
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

function words(element: HTMLElement): string {
  return (element.textContent ?? "").replace(/\s+/g, " ").trim();
}

// R8's first case: an address standing on its own reads as the target's current name.
test("an address standing on its own reads as the target's current name", async () => {
  const view = draw(<MarkdownBody markdown="Tied to dispatch://CORE-1 here." />);
  try {
    const link = await within(view.container).findByRole("link", { name: "CORE-1 Fix the thing" });
    expect(link).toHaveAttribute("href", "/issues/CORE-1");
  } finally {
    view.unmount();
  }
});

// R8's second case: a link whose words are its own address - the form a document stores a
// Markdown autolink in - reads as the title too, as the bare address does.
test("a link whose words are its own address reads as the target's current name", async () => {
  const view = draw(<MarkdownBody markdown="See [dispatch://CORE-1](dispatch://CORE-1)." />);
  try {
    const link = await within(view.container).findByRole("link", { name: "CORE-1 Fix the thing" });
    expect(link).toHaveAttribute("href", "/issues/CORE-1");
  } finally {
    view.unmount();
  }
});

// R8's third case, the only one that changes: a link whose words differ from its address keeps
// those words. The link still works and still carries the reference, so its hover card is the
// same card; only the words are the author's.
test("a link whose words its author wrote keeps those words", async () => {
  const view = draw(<MarkdownBody markdown="See [the rollout plan](dispatch://CORE-1) first." />);
  try {
    const link = await within(view.container).findByRole("link", { name: "the rollout plan" });
    expect(link).toHaveAttribute("href", "/issues/CORE-1");
    expect(link).toHaveAttribute("data-dispatch-ref", "dispatch://CORE-1");
    await waitFor(() => expect(words(view.container)).toBe("See the rollout plan first."));
  } finally {
    view.unmount();
  }
});

// R8 for an address that names nobody: a login written in any other form than a stored one is
// no node, so the words the writer wrote stand with no link at all. A `dispatch://` href opens
// no page in any browser, so the worded case is dropped as the bare one already is.
test("a worded link whose address cites nothing reads as its words, with no link", async () => {
  const view = draw(
    <MarkdownBody markdown="Ask [Dana](dispatch://person/Dana) about it." />,
    new QueryClient({ defaultOptions: { queries: { retry: false } } })
  );
  try {
    await waitFor(() => expect(words(view.container)).toBe("Ask Dana about it."));
    expect(within(view.container).queryByRole("link")).toBeNull();
    expect(view.container.querySelector("[data-dispatch-ref]")).toBeNull();
  } finally {
    view.unmount();
  }
});

// The plain-text projection reads the same reference the painted line reads, and names it the
// same way: a label and the line beside it never say two different things.
test("the plain-text projection names a reference by its resolved name", async () => {
  const view = draw(
    <PlainText markdown="Tied to **dispatch://CORE-1** here.">
      {(text) => <span data-testid="line">{text ?? ""}</span>}
    </PlainText>
  );
  try {
    await waitFor(() =>
      expect(view.getByTestId("line").textContent).toBe("Tied to CORE-1 Fix the thing here.")
    );
  } finally {
    view.unmount();
  }
});

const SESSION = "01a1058e-f14f-7684-87eb-3dc885955551";

/** The session a mention below names, as the registry lists it now: renamed since the mention
 *  was written as `@Old name`. */
const renamed: Agent = {
  capabilities: [],
  dir: "/workspaces/planner",
  host: "host-a",
  last_activity: null,
  last_seen: 1_700_000_000_000,
  machine_id: "host-a",
  open_asks: 0,
  roles: [],
  session_id: SESSION,
  state: "live",
  title: "Planner now",
};

/** The session holding the reviewer role, as the registry lists it: a role reference is named
 *  while a session it lists holds the role. */
const reviewing: Agent = {
  ...renamed,
  dir: "/workspaces/reviewer",
  roles: ["reviewer"],
  session_id: "01a1058e-f14f-7684-87eb-3dc885955552",
  title: "Reviewer",
};

/** A query client holding the registry's answer - the renamed session, and the session that
 *  holds the reviewer role - and the people Dispatch knows, of whom dana is one and nobody is
 *  not. */
function clientWithParties(): QueryClient {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["agents"], [renamed, reviewing]);
  client.setQueryData(["users"], [{ login: "dana@example.test" }]);
  return client;
}

// R4: a party's mention reads as the mention sign and the party's current name, a hand-written
// one as much as a picked one, so a session renamed since reads as it is called now. A worded
// link to the same party with no sign keeps its author's words (R8), and an `@` and a word with
// no link around it is no reference at all (R10). A session's live page names no node in the
// shared grammar (`pageRoutes` reads issue, document and agent-document pages alone), so a link
// to it is an ordinary link in its author's words, whatever they start with.
test("a party's mention reads as its current name, its other links keep their words, and a plain @ stays text", async () => {
  const page = `${window.location.origin}/agents/${SESSION}/live`;
  const view = draw(
    <MarkdownBody
      markdown={`Ask [@Old name](dispatch://agent/${SESSION}), [@Old](${page}), [the planner](dispatch://agent/${SESSION}) and @Planner today.`}
    />,
    clientWithParties()
  );
  try {
    const mention = await within(view.container).findByRole("link", { name: "@Planner now" });
    expect(mention).toHaveAttribute("href", `/agents/${SESSION}/live`);
    expect(within(view.container).getByRole("link", { name: "the planner" })).toHaveAttribute(
      "href",
      `/agents/${SESSION}/live`
    );
    expect(within(view.container).getByRole("link", { name: "@Old" })).toHaveAttribute(
      "href",
      page
    );
    expect(within(view.container).getAllByRole("link")).toHaveLength(3);
    expect(words(view.container)).toBe("Ask @Planner now, @Old, the planner and @Planner today.");
  } finally {
    view.unmount();
  }
});

// R5 and R8: a bare person reads as their login, with no link, where Dispatch knows the login;
// a login it does not know is the words written. A person's mention reads as the sign and the
// login. A bare role a session in the registry holds reads as its token, linked to its page.
test("a known person reads as their login with no link, an unknown one as written, and a held role as its token, linked", async () => {
  const view = draw(
    <MarkdownBody markdown="Ask dispatch://person/dana@example.test, [@Dana](dispatch://person/dana@example.test), dispatch://person/nobody@example.test and dispatch://role/reviewer." />,
    clientWithParties()
  );
  try {
    const role = await within(view.container).findByRole("link", { name: "reviewer" });
    expect(role).toHaveAttribute("href", "/agents?role=reviewer");
    await waitFor(() =>
      expect(words(view.container)).toBe(
        "Ask dana@example.test, @dana@example.test, dispatch://person/nobody@example.test and reviewer."
      )
    );
    expect(within(view.container).getAllByRole("link")).toEqual([role]);
  } finally {
    view.unmount();
  }
});

// R5 and R8: a role no session in the registry holds names nobody the picker would offer, so it
// reads as the words its writer wrote, with no link - bare or as a mention - as an unknown
// person does.
test("a role no session holds reads as the words written, with no link", async () => {
  const view = draw(
    <MarkdownBody markdown="Ask dispatch://role/release-captain, [@captain](dispatch://role/release-captain) and dispatch://role/reviewer." />,
    clientWithParties()
  );
  try {
    const role = await within(view.container).findByRole("link", { name: "reviewer" });
    await waitFor(() =>
      expect(view.container.querySelectorAll("[data-dispatch-missing]")).toHaveLength(2)
    );
    expect(words(view.container)).toBe(
      "Ask dispatch://role/release-captain, @captain and reviewer."
    );
    expect(within(view.container).getAllByRole("link")).toEqual([role]);
  } finally {
    view.unmount();
  }
});

// The plain-text projection reads parties as the painted line does: a mention as the sign and
// the current name, an author's own words as they are - a link to a session's live page among
// them, as the painted line keeps it - a role as its token and a known person as their login.
test("the plain-text projection names a mention, a role and a person as the painted line does", async () => {
  const page = `${window.location.origin}/agents/${SESSION}/live`;
  const view = draw(
    <PlainText
      markdown={`Ask [@Old name](dispatch://agent/${SESSION}), [@Old](${page}), [the planner](dispatch://agent/${SESSION}), dispatch://role/reviewer and dispatch://person/dana@example.test.`}
    >
      {(text) => <span data-testid="line">{text ?? ""}</span>}
    </PlainText>,
    clientWithParties()
  );
  try {
    await waitFor(() =>
      expect(view.getByTestId("line").textContent).toBe(
        "Ask @Planner now, @Old, the planner, reviewer and dana@example.test."
      )
    );
  } finally {
    view.unmount();
  }
});

// A mention's `@` is words its writer wrote, never code: a party's link whose words are all code
// is a worded link like any other, and keeps its words in the painted line and in the
// plain-text projection alike, rather than reading as the party's name in one and as code in
// the other.
test("a party's link whose words are code keeps them, painted and projected alike", async () => {
  const markdown = `Ask [\`@x\`](dispatch://agent/${SESSION}) now.`;
  const painted = draw(<MarkdownBody markdown={markdown} />, clientWithParties());
  const projected = draw(
    <PlainText markdown={markdown}>
      {(text) => <span data-testid="code-line">{text ?? ""}</span>}
    </PlainText>,
    clientWithParties()
  );
  try {
    // The session's title is in the cache, so the name is drawn by the time the link is.
    await waitFor(() =>
      expect(painted.container.querySelector(`a[href="/agents/${SESSION}/live"]`)).not.toBeNull()
    );
    expect(words(painted.container)).toBe("Ask @x now.");
    await waitFor(() => expect(projected.getByTestId("code-line").textContent).toBe("Ask @x now."));
  } finally {
    painted.unmount();
    projected.unmount();
  }
});

// The projection reads the whole text unless its caller asks for the first line alone, which a
// closed priority row does: the line is read from the parsed document, so its references are
// still named, and nothing after the first line is drawn or has its references named. A
// heading's line and a list's first line read the same way, and so does a block of HTML: its
// first line reads as its literal characters, which is how the projection draws HTML.
test("the plain-text projection reads the first line alone where it is asked to", async () => {
  const markdown = "Tied to **dispatch://CORE-1**\nhere.\n\n- A list after it";
  const view = draw(
    <>
      <PlainText firstLine markdown={markdown}>
        {(text) => <span data-testid="first">{text ?? ""}</span>}
      </PlainText>
      <PlainText markdown={markdown}>
        {(text) => <span data-testid="whole">{text ?? ""}</span>}
      </PlainText>
      <PlainText firstLine markdown={"## The plan\n\nIts first step."}>
        {(text) => <span data-testid="heading">{text ?? ""}</span>}
      </PlainText>
      <PlainText firstLine markdown={"\n- First item\n  and its next line\n- Second item"}>
        {(text) => <span data-testid="list">{text ?? ""}</span>}
      </PlainText>
      <PlainText
        firstLine
        markdown={"![Diagram](https://example.com/a.png)\nThe priority keeps one build."}
      >
        {(text) => <span data-testid="image">{text ?? ""}</span>}
      </PlainText>
      <PlainText firstLine markdown={"\\\nAfter a hard break\nand more"}>
        {(text) => <span data-testid="break">{text ?? ""}</span>}
      </PlainText>
      <PlainText firstLine markdown={"| Name | Owner |\n| --- | --- |\n| Fleet | Ops |"}>
        {(text) => <span data-testid="table">{text ?? ""}</span>}
      </PlainText>
      <PlainText firstLine markdown={"```\n\nfirst code\nsecond\n```"}>
        {(text) => <span data-testid="code">{text ?? ""}</span>}
      </PlainText>
      <PlainText
        firstLine
        markdown={'<img src="https://example.com/a.png">\nThe priority keeps one build.'}
      >
        {(text) => <span data-testid="html-image">{text ?? ""}</span>}
      </PlainText>
      <PlainText
        firstLine
        markdown={"<details>\n<summary>Why this priority</summary>\nThe fleet first.\n</details>"}
      >
        {(text) => <span data-testid="html-details">{text ?? ""}</span>}
      </PlainText>
      <PlainText firstLine markdown={"<br>\nThe next line."}>
        {(text) => <span data-testid="html-break">{text ?? ""}</span>}
      </PlainText>
    </>
  );
  try {
    await waitFor(() =>
      expect(view.getByTestId("first").textContent).toBe("Tied to CORE-1 Fix the thing")
    );
    await waitFor(() => expect(view.getByTestId("heading").textContent).toBe("The plan"));
    await waitFor(() => expect(view.getByTestId("list").textContent).toBe("First item"));
    // A line that holds no words - an image alone, a hard break - is passed over, a table's
    // first row reads whole, and a code block's first line is its first line that holds words.
    await waitFor(() =>
      expect(view.getByTestId("image").textContent).toBe("The priority keeps one build.")
    );
    await waitFor(() => expect(view.getByTestId("break").textContent).toBe("After a hard break"));
    await waitFor(() => expect(view.getByTestId("table").textContent).toBe("Name Owner"));
    await waitFor(() => expect(view.getByTestId("code").textContent).toBe("first code"));
    // A block of HTML is one node holding all of its lines, and its first line is its first
    // line's literal characters, as the projection draws them: never blank, and never the
    // lines after it.
    await waitFor(() =>
      expect(view.getByTestId("html-image").textContent).toBe(
        '<img src="https://example.com/a.png">'
      )
    );
    await waitFor(() => expect(view.getByTestId("html-details").textContent).toBe("<details>"));
    await waitFor(() => expect(view.getByTestId("html-break").textContent).toBe("<br>"));
    await waitFor(() =>
      expect(view.getByTestId("whole").textContent).toBe(
        "Tied to CORE-1 Fix the thing here. A list after it"
      )
    );
  } finally {
    view.unmount();
  }
});

// Shape 2: inside a link or a button there is no link of its own. The words and the reference
// stay - the hover card is still the card - and the host is what a click follows.
test("a reference inside a link or a button is drawn inert", async () => {
  const view = draw(
    <ReferenceHostIs host="control">
      <MarkdownBody markdown="Tied to dispatch://CORE-1 here." />
    </ReferenceHostIs>
  );
  try {
    await waitFor(() => expect(words(view.container)).toBe("Tied to CORE-1 Fix the thing here."));
    expect(view.container.querySelector("a")).toBeNull();
    const inert = view.container.querySelector("[data-markdown-link]");
    expect(inert).toHaveAttribute("data-dispatch-ref", "dispatch://CORE-1");
  } finally {
    view.unmount();
  }
});

// Shape 4: a suggestion's replacement shows the exact characters it swaps, so no title is drawn
// over a reference there - the reader has to see what accepting it would write.
test("a reference in a diff is drawn as its characters", async () => {
  const view = draw(
    <ReferenceHostIs host="diff">
      <MarkdownBody markdown="Tied to **dispatch://CORE-1** here." variant="inline" />
    </ReferenceHostIs>
  );
  try {
    expect(words(view.container)).toBe("Tied to **dispatch://CORE-1** here.");
    expect(view.container.querySelector("a")).toBeNull();
    expect(view.container.querySelector("strong")).toBeNull();
  } finally {
    view.unmount();
  }
});

const threadRow = {
  kind: "comment",
  reply: { author: { id: "planner", kind: "session" }, created_at: "2026-01-01T00:00:00Z" },
  ref: "dispatch://CORE-1/comment/comment-1",
  subject: "## Ship **behind a flag**, or wait?",
  thread_id: "comment-1",
  unread: false,
  unread_count: 0,
} as unknown as MyReplyRow;

// The Inbox's own line is drawn by the renderer, not stripped by a regular expression: the
// heading's words format as the thread formats them, and no syntax is left standing.
test("an Inbox thread title draws its line through the renderer", async () => {
  const view = draw(<ThreadTitle className="text-sm" thread={threadRow} />);
  try {
    await waitFor(() =>
      expect(view.container.querySelector("strong")?.textContent).toBe("behind a flag")
    );
    expect(words(view.container)).toBe("Ship behind a flag, or wait?");
    // One line, never the heading's own block.
    expect(view.container.querySelector("h1, h2, h3, p, ul")).toBeNull();
  } finally {
    view.unmount();
  }
});
