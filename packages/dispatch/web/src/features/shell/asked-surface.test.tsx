import { expect, test } from "bun:test";
// biome-ignore lint/style/noRestrictedImports: the text the box holds is set with one change event, since `user.keyboard` into that uncontrolled box makes React print its unawaited-act message in this environment; the keys a browser types into it are `e2e/keyboard-issue.e2e.ts`'s
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useEffect, useState } from "react";
import { MemoryRouter } from "react-router-dom";

import { quietConsoleError } from "../../__tests__/act-warnings";
import { type AskedSurfaceProps, askedSurface } from "./asked-surface";
import { type Chunk, chunk } from "./chunk-component";
import { chunkLoadsOut, onlyPrefetchesOut } from "./chunk-loads";

/** A surface as the bar and the peek are: mounted while closed, drawing nothing then. */
function Surface({ open, typed = "" }: AskedSurfaceProps): ReactNode {
  return open === false ? null : <p>Surface with {JSON.stringify(typed)}</p>;
}

/** The shell's part: an opener, and the surface it opens. */
function Shell({ source }: { source: Chunk<AskedSurfaceProps> }): ReactNode {
  const [Asked] = useState(() => askedSurface(source));
  const [open, setOpen] = useState(false);
  return (
    <MemoryRouter>
      <button onClick={() => setOpen(true)} type="button">
        Open
      </button>
      <Asked onClose={() => setOpen(false)} open={open} />
    </MemoryRouter>
  );
}

// A surface whose chunk is in opens in the commit of the press that asked for it, so the next key
// the reader types meets the surface and not the page.
test("a surface whose chunk is in draws in the commit of the press", async () => {
  const user = userEvent.setup();
  const source = chunk(async () => Surface);
  await source.load();
  const seen: string[] = [];
  function Watch({ open }: { open: boolean }): ReactNode {
    useEffect(() => {
      if (open) seen.push(document.body.textContent ?? "");
    }, [open]);
    return null;
  }
  const Asked = askedSurface(source);
  function Opener(): ReactNode {
    const [open, setOpen] = useState(false);
    return (
      <MemoryRouter>
        <button onClick={() => setOpen(true)} type="button">
          Open
        </button>
        <Asked onClose={() => setOpen(false)} open={open} />
        <Watch open={open} />
      </MemoryRouter>
    );
  }
  const view = render(<Opener />);
  try {
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(seen).toEqual(['OpenSurface with ""']);
    expect(screen.queryByRole("textbox", { name: "Loading" })).not.toBeInTheDocument();
  } finally {
    view.unmount();
  }
});

// While the chunk is out, the box holds the focus, and what the reader types there is the
// surface's when it lands.
test("while the chunk is out a box holds the focus, and what is typed into it reaches the surface", async () => {
  const user = userEvent.setup();
  const { promise, resolve } = Promise.withResolvers<typeof Surface>();
  const view = render(<Shell source={chunk(() => promise)} />);
  try {
    await user.click(screen.getByRole("button", { name: "Open" }));
    const box = screen.getByRole("textbox", { name: "Loading" });
    expect(box).toHaveFocus();
    // What the box holds as the chunk lands is what this row is about; the keys a browser types
    // into it are `e2e/keyboard-issue.e2e.ts`'s.
    fireEvent.change(box, { target: { value: "1e" } });
    await act(async () => resolve(Surface));
    expect(screen.getByText('Surface with "1e"')).toBeDefined();
    expect(screen.queryByRole("textbox", { name: "Loading" })).not.toBeInTheDocument();
  } finally {
    resolve(Surface);
    view.unmount();
  }
});

// Escape calls the open off: the focus goes back to the opener, and a chunk that lands after that
// opens nothing.
test("Escape in the box calls the open off, and the chunk that lands after opens nothing", async () => {
  const user = userEvent.setup();
  const { promise, resolve } = Promise.withResolvers<typeof Surface>();
  const view = render(<Shell source={chunk(() => promise)} />);
  try {
    const opener = screen.getByRole("button", { name: "Open" });
    await user.click(opener);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("textbox", { name: "Loading" })).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
    await act(async () => resolve(Surface));
    expect(screen.queryByText(/Surface with/)).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  } finally {
    resolve(Surface);
    view.unmount();
  }
});

// A press elsewhere takes the focus out of the box, and that calls the open off too.
test("the focus leaving the box calls the open off", async () => {
  const user = userEvent.setup();
  const { promise, resolve } = Promise.withResolvers<typeof Surface>();
  const view = render(
    <>
      <Shell source={chunk(() => promise)} />
      <input aria-label="Elsewhere" />
    </>
  );
  try {
    await user.click(screen.getByRole("button", { name: "Open" }));
    const elsewhere = screen.getByRole("textbox", { name: "Elsewhere" });
    await user.click(elsewhere);
    expect(screen.queryByRole("textbox", { name: "Loading" })).not.toBeInTheDocument();
    await act(async () => resolve(Surface));
    expect(screen.queryByText(/Surface with/)).not.toBeInTheDocument();
    expect(elsewhere).toHaveFocus();
  } finally {
    resolve(Surface);
    view.unmount();
  }
});

// A download that fails online closes the open, so the next press asks for the chunk again
// rather than finding the surface open with nothing drawn.
test("a chunk that fails to download closes the open, and the next press asks for it again", async () => {
  const user = userEvent.setup();
  let imports = 0;
  const source = chunk(async () => {
    imports += 1;
    if (imports === 1) throw new TypeError("Failed to fetch dynamically imported module");
    return Surface;
  });
  const view = render(<Shell source={source} />);
  try {
    await user.click(screen.getByRole("button", { name: "Open" }));
    await act(async () => undefined);
    expect(screen.queryByRole("textbox", { name: "Loading" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByText('Surface with ""')).toBeDefined();
    expect(imports).toBe(2);
  } finally {
    view.unmount();
  }
});

// A render under the surface that throws - a chunk the surface draws from failing - closes the
// surface and leaves the shell drawn; the next open draws it afresh.
test("a surface whose render throws closes, and the next open draws it again", async () => {
  const user = userEvent.setup();
  let throws = true;
  function Fragile({ open }: AskedSurfaceProps): ReactNode {
    if (open !== false && throws)
      throw new TypeError("Failed to fetch dynamically imported module");
    return open === false ? null : <p>Fragile drawn</p>;
  }
  const source = chunk(async () => Fragile);
  await source.load();
  const logged = quietConsoleError();
  const view = render(<Shell source={source} />);
  try {
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(screen.getByRole("button", { name: "Open" })).toBeDefined();
    expect(screen.queryByText("Fragile drawn")).not.toBeInTheDocument();
    expect(logged).toHaveBeenCalled();
    throws = false;
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(screen.getByText("Fragile drawn")).toBeDefined();
  } finally {
    logged.mockRestore();
    view.unmount();
  }
});

// An open the reader called off leaves its download to finish as a prefetch: nobody waits on it
// any more, so its failure - once the network is back after an outage the reader called the open
// off in, say - is no failure a reader is in front of, and the chunk-failure recovery reads it as
// a prefetch's and reloads nothing (`onlyPrefetchesOut`, `DeploymentResilience.tsx`). Counted as
// a render until it ended, it would spend one of the tab's reloads under a reader who had gone
// on.
test("an open called off while its chunk is out leaves the download to finish as a prefetch", async () => {
  const user = userEvent.setup();
  // From a page with no download out: the counts are the module's, for the page, and a download
  // an earlier row of this file started and did not wait for, or a row of an earlier run of it
  // (`--rerun-each`, which keeps the file's modules), would decide this one. A row of another file
  // cannot: `--parallel` gives every file modules of its own.
  await waitFor(() => expect(chunkLoadsOut()).toBe(0), { timeout: 30_000 });
  const { promise, reject } = Promise.withResolvers<typeof Surface>();
  const view = render(<Shell source={chunk(() => promise)} />);
  try {
    await user.click(screen.getByRole("button", { name: "Open" }));
    expect(onlyPrefetchesOut(), "the open's download was not counted as a render").toBe(false);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("textbox", { name: "Loading" })).not.toBeInTheDocument();
    expect(chunkLoadsOut(), "the called-off download was not left to finish").toBe(1);
    expect(onlyPrefetchesOut(), "the called-off download still counts as a render").toBe(true);
    await act(async () => reject(new TypeError("Failed to fetch dynamically imported module")));
    expect(chunkLoadsOut()).toBe(0);
  } finally {
    reject(new TypeError("Failed to fetch dynamically imported module"));
    view.unmount();
  }
});
