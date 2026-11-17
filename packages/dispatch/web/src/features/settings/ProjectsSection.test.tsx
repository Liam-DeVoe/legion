import { expect, jest, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: the submit site below is a form submitted with no control pressed, which user-event has no form for
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { realClock, tick } from "../../__tests__/fake-clock";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { listOf, panelOf, textBoxes } from "../../__tests__/list-picks";
import { userOnFakeClock } from "../../__tests__/user";
import { ApiError, api } from "../../api/client";
import type { AuthenticatedUser, DispatchUser, Project } from "../../api/types";
import { ProjectsSection } from "./ProjectsSection";

const core: Project = {
  created_at: "2026-09-01T00:00:00Z",
  key: "CORE",
  name: "Core",
  open_asks: 0,
  gated_steps: [],
};

// Dispatch made the project and lost its answer (`409 KEY_COMMITTED`): the form lets go of the key
// and name and says what was made, the list is read again, and a second press - with nothing in
// the form - creates nothing, where the old draft would have tried the same project again.
test("a create answered KEY_COMMITTED clears the form, says the project was made, and a second press sends nothing", async () => {
  jest.useFakeTimers();
  const user = userOnFakeClock();
  const listProjects = spyOn(api, "listProjects").mockResolvedValue([core]);
  const listDefaults = spyOn(api, "listProjectDefaultAssignees").mockResolvedValue([]);
  const createProject = spyOn(api, "createProject").mockRejectedValue(
    new ApiError(409, { code: "KEY_COMMITTED", error: "already made" })
  );
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });

  try {
    render(
      <QueryClientProvider client={queryClient}>
        <ProjectsSection />
      </QueryClientProvider>
    );
    await screen.findByRole("cell", { name: "CORE" });
    const key = screen.getByLabelText<HTMLInputElement>("Key");
    await user.click(key);
    await user.paste("qa");
    await user.click(screen.getByLabelText("Name"));
    await user.paste("Quality");
    await user.click(screen.getByRole("button", { name: "New project" }));

    const made = await screen.findByTestId("landed-without-answer");
    expect(made.textContent).toBe(
      "Project QA was made, but Dispatch lost its answer, so it may take a moment to appear."
    );
    expect(key.value).toBe("");
    expect(screen.getByLabelText<HTMLInputElement>("Name").value).toBe("");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await waitFor(() => expect(listProjects.mock.calls.length).toBeGreaterThan(1));
    // The project list carries no default: the new project's is read from the defaults read.
    await waitFor(() => expect(listDefaults.mock.calls.length).toBeGreaterThan(1));

    // The form submitted with no control pressed: user-event has no form for submit.
    fireEvent.submit(key.closest("form") as HTMLFormElement);
    await tick(20);
    expect(createProject).toHaveBeenCalledTimes(1);
  } finally {
    realClock();
    cleanup();
    queryClient.clear();
    listProjects.mockRestore();
    listDefaults.mockRestore();
    createProject.mockRestore();
  }
});

/** The section over CORE, which names alice until a save names another, with `readUsers`
 *  answering the people list and `whoAmI` the viewer; every create and default-assignee save is
 *  recorded. */
function renderSection({
  readUsers = () => Promise.resolve([{ login: "alice" }, { login: "bob" }, { login: "carol" }]),
  whoAmI = () => Promise.resolve({ event_head: "0", kind: "user", login: "alice" }),
}: {
  readUsers?: () => Promise<DispatchUser[]>;
  whoAmI?: () => Promise<AuthenticatedUser>;
} = {}) {
  let defaults = [{ assignee: "alice", project: "CORE" }];
  const stubs = {
    createProject: spyOn(api, "createProject").mockImplementation(async (input) => ({
      ...core,
      default_assignee: input.default_assignee ?? "alice",
      key: input.key,
      name: input.name,
    })),
    listDefaults: spyOn(api, "listProjectDefaultAssignees").mockImplementation(
      async () => defaults
    ),
    listProjects: spyOn(api, "listProjects").mockResolvedValue([core]),
    listUsers: spyOn(api, "listUsers").mockImplementation(readUsers),
    putDefault: spyOn(api, "putProjectDefaultAssignee").mockImplementation(
      async (project, { assignee }) => {
        defaults = [...defaults.filter((row) => row.project !== project), { assignee, project }];
        return { assignee, project };
      }
    ),
    whoAmI: spyOn(api, "whoAmI").mockImplementation(whoAmI),
  };
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <ProjectsSection />
    </QueryClientProvider>
  );
  return {
    ...stubs,
    done: () => {
      cleanup();
      queryClient.clear();
      for (const stub of Object.values(stubs)) stub.mockRestore();
    },
  };
}

// INFRA-3135, R3: "Every list of options a reader picks from holds a text box that narrows the
// options." The New project form's Default assignee offers everyone who has signed in, who grow
// with the deployment: opened from its trigger it holds a text box of its own, says the people
// are loading while their read is out, then a word typed there leaves only the people it
// matches, and the pick is the person the project is created with, with the focus back on the
// trigger.
test("the New project form's Default assignee list holds a text box that narrows the people, and a pick is who the project names", async () => {
  const user = userEvent.setup();
  const people = Promise.withResolvers<DispatchUser[]>();
  const view = renderSection({ readUsers: () => people.promise });
  try {
    await screen.findByRole("cell", { name: "CORE" });
    const trigger = screen.getByLabelText("Default assignee");
    await waitFor(() => expect(trigger).toHaveTextContent("alice"));
    const before = new Set(textBoxes());
    await user.click(trigger);
    const own = textBoxes().filter((box) => !before.has(box));
    expect(
      own,
      "the New project form's Default assignee list holds no text box of its own"
    ).toHaveLength(1);
    const list = listOf(own[0] as HTMLElement);
    await user.type(own[0] as HTMLElement, "car");
    // Until the people are read the list holds the viewer alone, whom "car" leaves out.
    expect(list).toHaveTextContent("Loading people…");

    await act(async () => {
      people.resolve([{ login: "alice" }, { login: "bob" }, { login: "carol" }]);
      await people.promise;
    });
    await waitFor(() =>
      expect(
        within(list)
          .getAllByRole("option")
          .map((option) => option.textContent)
      ).toEqual(["carol"])
    );
    await user.keyboard("{Enter}");
    expect(trigger).toHaveFocus();
    expect(trigger).toHaveTextContent("carol");

    await user.click(screen.getByLabelText("Key"));
    await user.paste("qa");
    await user.click(screen.getByLabelText("Name"));
    await user.paste("Quality");
    await user.click(screen.getByRole("button", { name: "New project" }));
    await waitFor(() =>
      expect(view.createProject).toHaveBeenCalledWith(
        { default_assignee: "carol", key: "QA", name: "Quality" },
        keyedAttempt
      )
    );
  } finally {
    view.done();
  }
});

// INFRA-3135, R3, for each project's Default assignee: the same people, in a list of its own per
// row. A word leaves only the people it matches, Escape closes the list with nothing picked and
// nothing saved, and a pick saves the project's default with the focus back on the trigger.
test("a project's Default assignee list holds a text box that narrows the people, and a pick saves the default", async () => {
  const user = userEvent.setup();
  const view = renderSection();
  try {
    const trigger = await screen.findByLabelText("Default assignee of CORE");
    await waitFor(() => expect(trigger).toHaveTextContent("alice"));
    const before = new Set(textBoxes());
    await user.click(trigger);
    const own = textBoxes().filter((box) => !before.has(box));
    expect(own, "the Default assignee of CORE list holds no text box of its own").toHaveLength(1);
    const list = listOf(own[0] as HTMLElement);
    await within(list).findByRole("option", { name: "carol" });
    await user.type(own[0] as HTMLElement, "bo");
    expect(
      within(list)
        .getAllByRole("option")
        .map((option) => option.textContent)
    ).toEqual(["bob"]);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox", { name: "Default assignee of CORE options" })).toBeNull();
    expect(trigger).toHaveFocus();
    expect(trigger).toHaveTextContent("alice");

    await user.click(trigger);
    await user.type(within(panelOf(trigger)).getByRole("combobox"), "bo");
    await user.keyboard("{Enter}");
    expect(trigger).toHaveFocus();
    await waitFor(() => expect(trigger).toHaveTextContent("bob"));
    await waitFor(() =>
      expect(view.putDefault).toHaveBeenCalledWith("CORE", { assignee: "bob" }, keyedAttempt)
    );
    expect(view.putDefault).toHaveBeenCalledTimes(1);
  } finally {
    view.done();
  }
});

// A pick of the person a project already names saves nothing, as the select the list replaced
// fired nothing for the value it held; and a list whose people could not be read says so.
test("a project's Default assignee list says the people could not be loaded, and a pick of the person it names saves nothing", async () => {
  const user = userEvent.setup();
  const view = renderSection({ readUsers: () => Promise.reject(new Error("people unreachable")) });
  try {
    const trigger = await screen.findByLabelText("Default assignee of CORE");
    await waitFor(() => expect(trigger).toHaveTextContent("alice"));
    await user.click(trigger);
    const panel = panelOf(trigger);
    expect(await within(panel).findByText("Could not load the people list.")).toBeTruthy();
    expect(within(panel).queryByText("Loading people…")).toBeNull();
    await user.click(within(panel).getByRole("option", { name: "alice" }));
    expect(trigger).toHaveFocus();
    await act(async () => {});
    expect(view.putDefault).not.toHaveBeenCalled();
  } finally {
    view.done();
  }
});

// The Default assignee list is a button, which no `required` holds: while the viewer has not
// resolved it names nobody and takes no pick, and the form files no project then, whatever
// submits it.
test("with no default assignee picked, New project files nothing", async () => {
  const user = userEvent.setup();
  const view = renderSection({ whoAmI: () => new Promise(() => {}) });
  try {
    await screen.findByRole("cell", { name: "CORE" });
    expect(screen.getByLabelText("Default assignee")).toBeDisabled();
    await user.click(screen.getByLabelText("Key"));
    await user.paste("qa");
    await user.click(screen.getByLabelText("Name"));
    await user.paste("Quality");
    await user.click(screen.getByRole("button", { name: "New project" }));
    await act(async () => {});
    expect(view.createProject, "a project was created naming nobody").not.toHaveBeenCalled();
  } finally {
    view.done();
  }
});
