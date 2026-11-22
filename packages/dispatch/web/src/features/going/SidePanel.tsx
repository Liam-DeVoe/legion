import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useLocation } from "react-router-dom";

import { whoAmIQuery } from "../../api/queries";
import type { GoingContextInput } from "../../api/types";
import { issueProject } from "../../lib/issue-key";
import { borderDefault, card, dangerText, inputClasses } from "../../theme/classes";
import { nodeRouteFromHref, routeProjectOf } from "../refs/routes";
import { issueOfPage } from "../search/bar-scope";
import { ConversationView } from "./ConversationView";
import {
  asksPerson,
  type ClickedItem,
  movePanel,
  newConversation,
  setClicked,
  setPlace,
  useGoing,
  useSendQueued,
} from "./going-store";
import { History } from "./History";
import { usePlanCardKeys } from "./PlanRows";
import { BoxRow, boxHint, boxWords, ConversationHeader } from "./RunControls";
import { useAsk } from "./useAsk";

/** The side panel's element id. */
const SIDE_PANEL_ID = "going-side-panel";

/** The item a click on the page was on: the issue or document a link or a reference in it names,
 *  as the context of a request names it; undefined for a click on nothing of the kind. */
function clickedItem(target: EventTarget | null): GoingContextInput | undefined {
  if (!(target instanceof Element)) {
    return undefined;
  }
  const holder = target.closest<HTMLElement>("[data-dispatch-ref], a[href]");
  const reference =
    holder?.getAttribute("data-dispatch-ref") ?? holder?.getAttribute("href") ?? undefined;
  if (reference === undefined) {
    return undefined;
  }
  const route = nodeRouteFromHref(reference);
  if (route === undefined) {
    return undefined;
  }
  if ("key" in route) {
    return { issue: route.key, project: issueProject(route.key) };
  }
  return "project" in route ? { project: route.project } : undefined;
}

/** The context of the next request at the side: the page the panel sits beside, and the item a
 *  click on that page named, which takes the place of the page's own issue and project while the
 *  person is on it: a click on a project's document names no issue. */
function contextOf(
  pathname: string,
  search: string,
  clicked: ClickedItem | undefined
): GoingContextInput {
  const page: GoingContextInput = {
    block: "",
    issue: issueOfPage(pathname, search) ?? "",
    path: pathname,
    project: routeProjectOf(pathname) ?? "",
  };
  if (clicked === undefined || clicked.path !== pathname) {
    return page;
  }
  return { ...page, issue: "", ...clicked.context };
}

/**
 * The conversation kept at the side of the page (R1): a panel at the right that stays open on
 * every navigation and leaves the page usable, its box at its foot asking with the page as the
 * context, and a click on the page naming the item of the next request. Its box is the bar's
 * (design v2, state 2): the context chip, the level beside it, Stop while the run works, the
 * plan card's keys while a plan waits, and words sent during a run held until it ends. Ctrl-K
 * brings the conversation over the page, Ctrl-Shift-K and Close close it, and Esc in its empty box
 * stops a run that works.
 */
export function SidePanel(): ReactNode {
  const going = useGoing();
  const location = useLocation();
  const login = useQuery(whoAmIQuery()).data?.login;
  const [words, setWords] = useState("");
  const [history, setHistory] = useState(false);
  const panelRef = useRef<HTMLElement>(null);
  const side = going.place === "side";
  const context = contextOf(location.pathname, location.search, going.clicked);
  const asking = useAsk(context);
  const cardKeys = usePlanCardKeys();
  // Words sent while the run worked go once it no longer works, from the panel while it shows the
  // conversation (the bar sends them while it is over the page).
  useSendQueued((held) => side && asking.enter(held));

  // A click on the page, outside the panel, works as usual, and the item it was on is the
  // context of the next request.
  useEffect(() => {
    if (!side) {
      return;
    }
    const onClick = (event: MouseEvent) => {
      if (panelRef.current?.contains(event.target as Node) === true) {
        return;
      }
      const item = clickedItem(event.target);
      movePanel({ kind: "page_click" }, login);
      if (item !== undefined) {
        setClicked(item, window.location.pathname);
      }
    };
    document.addEventListener("click", onClick, true);
    return () => document.removeEventListener("click", onClick, true);
  }, [login, side]);

  if (!side) {
    return null;
  }
  return (
    <aside
      aria-label="Command bar at the side"
      className={`fixed top-0 right-0 bottom-0 z-40 flex w-full flex-col border-l shadow-2xl sm:w-96 ${card} ${borderDefault}`}
      data-testid="going-side-panel"
      id={SIDE_PANEL_ID}
      ref={panelRef}
    >
      <ConversationHeader
        history={history}
        move="Over the page"
        onClose={() => setPlace("closed")}
        onHistory={() => setHistory((shown) => !shown)}
        onMove={() => setPlace("over")}
        onNew={() => {
          newConversation();
          setHistory(false);
        }}
      />
      {history ? (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <History onOpen={() => setHistory(false)} />
        </div>
      ) : (
        <ConversationView
          answering={asking.answering}
          onContinue={asking.onContinue}
          onRetry={asking.retry}
        />
      )}
      <div className={`shrink-0 border-t p-3 ${borderDefault}`}>
        <BoxRow
          box={
            <input
              aria-label="Ask the command bar"
              className={`block min-w-48 flex-1 rounded-lg border px-3 py-2 text-sm outline-none ${inputClasses(true)}`}
              onChange={(event) => setWords(event.target.value)}
              onKeyDown={(event) => {
                if (cardKeys(event)) {
                  return;
                }
                if (event.key === "Enter" && !event.shiftKey) {
                  if (asking.enter(words)) {
                    event.preventDefault();
                    setWords("");
                  }
                } else if (event.key === "Escape") {
                  asking.escape(words);
                  event.preventDefault();
                }
              }}
              placeholder={boxWords({
                asked: asksPerson(going),
                open: going.conversation !== undefined || going.starting !== undefined,
                planWaits: asking.planWaits || asking.runWaits,
                search: false,
              })}
              type="text"
              value={words}
            />
          }
          chip={context.issue !== "" && context.issue !== undefined ? context.issue : "this page"}
          hint={boxHint(asking, words)}
          onStop={asking.stop}
        />
        {going.refusal === undefined ? null : (
          <p className={`mt-2 text-xs ${dangerText}`} role="alert">
            {going.refusal}
          </p>
        )}
      </div>
    </aside>
  );
}
