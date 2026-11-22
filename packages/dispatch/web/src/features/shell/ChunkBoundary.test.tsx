import { expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";

import { quietConsoleError } from "../../__tests__/act-warnings";
import { ChunkBoundary } from "./ChunkBoundary";

function Throwing(): never {
  throw new TypeError("Failed to fetch dynamically imported module");
}

// A part whose chunk failed draws nothing in its place, or what its host names for it, and the
// rest of the page stays drawn: the throw never reaches a boundary above.
test("a render that throws under it draws nothing, or the fallback a host names, and its siblings stay", () => {
  const logged = quietConsoleError();
  const view = render(
    <div>
      <p>Signed in as alice</p>
      <section aria-label="Quiet">
        <ChunkBoundary>
          <Throwing />
        </ChunkBoundary>
      </section>
      <ChunkBoundary fallback={<p>The shell before</p>}>
        <Throwing />
      </ChunkBoundary>
    </div>
  );
  try {
    expect(screen.getByRole("region", { name: "Quiet" })).toBeEmptyDOMElement();
    expect(screen.getByText("The shell before")).toBeTruthy();
    expect(screen.getByText("Signed in as alice")).toBeTruthy();
    expect(logged).toHaveBeenCalled();
  } finally {
    logged.mockRestore();
    view.unmount();
  }
});

test("a part that renders draws as it is", () => {
  const view = render(
    <ChunkBoundary fallback={<p>The shell before</p>}>
      <p>content</p>
    </ChunkBoundary>
  );
  try {
    expect(screen.getByText("content")).toBeTruthy();
    expect(screen.queryByText("The shell before")).not.toBeInTheDocument();
  } finally {
    view.unmount();
  }
});
