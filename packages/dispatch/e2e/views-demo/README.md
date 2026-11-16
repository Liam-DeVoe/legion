# Views demo

A live demo of the Issues tab's views (dispatch://INFRA-3221): the Dispatch web app, run by Vite,
with a stubbed back end in the browser that answers every read from fixtures built out of
production reads. It shows the Issues tab and the priority pages (cybermonklabs/legion#3767),
whose issue tables are the same table. It is a prototype for a spec's approval, not part of the
browser suite.

The fixtures, the pictures and the single file are written outside the repository. Production
data (issue titles, keys, people) never goes into a file here.

## Files

- `web/vite.demo.config.ts`: Dispatch's Vite config with the stub put into the page before the
  app's entry (`web/src/demo/install.ts`), so no app file imports demo code. The dev server
  serves the fixtures from `DISPATCH_DEMO_FIXTURES` and proxies nothing.
- `web/src/demo/`: the stub.
  - `clock.ts` fixes the page's clock: the page reads the time as the fixtures' read time
    (`manifest.daemon_read_at`) plus the time since it loaded, so every age (Time waiting, Time
    in Legion phase, Closed) and the Closed filter's windows on Done look the same whenever the
    demo is opened.
    `vite.demo.config.ts` writes that time into the page before any module runs.
  - `install.ts` replaces `fetch` and `WebSocket`.
  - `server.ts` answers every `/api/` and `/auth/` request the page makes. The listing applies
    every filter the server applies. Writes change its state, which the tab keeps in
    sessionStorage. A write goes out on the event feed as the server's event would.
  - `priorities.ts` answers the priorities (`GET /api/v1/priorities`, with `summary=true` too,
    one priority's history, and the two priority writes) and the listing's `advances` and
    `moved` filters, as main's `api/priority_queries.go` and `api/issue_queries.go` read them.
    Every count is read from the issues as the stub holds them, so a write moves them.
  - `wire.ts` writes a time, a rank and the status order as the server writes them.
  - `live.ts` is `/ws/live`: the `legion` view gets the board, the event feed gets the stub's
    events.
  - `banner.ts` is the note at the foot of every page that names what is real, estimated and
    invented.
  - `file-router.ts` is the single file's router.
- `e2e/views-demo/`:
  - `fixtures.ts` builds the fixtures.
  - `production.ts` makes and keeps the production reads.
  - `board-rules.ts` holds the board route's rules, ported from the Go of cybermonklabs/legion#3578
    (`legion/board_rules.go`, `legion/waiting_line.go`, `legion/tree.go`,
    `api/legion_board.go`).
  - `phase-facts.ts` derives the per-issue facts from Dispatch events, by the metrics' rule for
    an issue the sampler never observed.
  - `shots.ts` is the screenshot run.
  - `single-file.ts` builds the single HTML file.

## Build the fixtures

The reads are GETs only, made one at a time and at most five a second. A second run reads only
what `<out>/raw/` lacks. Add `--refresh` to read everything again.

```sh
cd packages/dispatch
DISPATCH_URL=<Dispatch base URL> DISPATCH_TOKEN_FILE=<token file> \
  bun e2e/views-demo/fixtures.ts --out "$TMPDIR/views-demo/fixtures" \
  --project <KEY> --reader <login> --daemon-url <the project's Legion daemon base URL> \
  --no-daemon-project <KEY> --priorities <file>
```

`--project` is the project with a Legion daemon. `--daemon-url` reads its daemon's
`GET /legion/v1/state` (in the cluster, no token). Use `--daemon-state <file>` instead to build
from a saved state. Each `--no-daemon-project` adds a real project's open and done listings and
no Legion setting, so the views of a project with no daemon can be shown beside it; its done rows
keep a null `closed_at`, since their events are not read. `<out>/manifest.json` names, for each
fixture, its source, the UTC time it was read, and which of its fields are real, derived or
invented. The board leaves out a card's `checks_verdict`, which
cybermonklabs/legion#2731's newest head removes.

### The priorities

Production's build has no priorities, so they are invented. `--priorities` names a file, kept
outside the repository since it names real issues, of this shape:

```json
{
  "made_at": "<when the first priority was written, UTC>",
  "priorities": [
    { "title": "…", "owner": "…", "description": "…", "issues": ["<KEY>-<n>", "…"] }
  ]
}
```

The priorities take that order, each written 15 minutes after the one before it by the reader.
Each issue named states an `advances` relation on its priority, a minute apart, and every issue
below it inherits it. Every count on the priority pages follows from those issues as they stand.
The one production read this adds is the events of the priority work standing in backlog or
icebox, for when each was moved there. `priorities.json` holds the priorities, the relations and
the moves, and the manifest names all three as invented or derived. Without `--priorities` the
pages say that no priority is live.

## The fixtures as an archive

The files the stub reads, the manifest with them, are also kept as one archive (it holds
production data, so it stays outside the repository). To run the demo or the pictures from it,
unpack it into a directory and name that directory as below:

```sh
mkdir -p "$TMPDIR/views-demo/fixtures"
tar -xzf <the archive>.tar.gz -C "$TMPDIR/views-demo/fixtures"
```

To make the archive again from a fixtures directory:

```sh
tar -czf <the archive>.tar.gz -C "$TMPDIR/views-demo/fixtures" manifest.json issues.json \
  population.json projects.json reader.json schema.json agents.json priorities.json
```

## Run the demo

```sh
cd packages/dispatch/web
DISPATCH_DEMO_FIXTURES="$TMPDIR/views-demo/fixtures" \
  bunx vite --config vite.demo.config.ts --port 5190 --host 127.0.0.1
```

Open `http://127.0.0.1:5190/projects/<KEY>/issues`, or `/priorities`. A request the stub cannot
answer is logged in the console (`[views demo] no stub route answers …`) and kept in
`window.__dispatchDemoUnanswered`.

To show a failed read of the daemon, add `demo=read-failed` to the address
(`/projects/<KEY>/issues?view=open&demo=read-failed`; in the single file, after the `#`). The
board's newest read is then a failure with an error text the demo makes up, its cards those of
the read before it, so the alert under the tabs shows; the banner names the failure among what
is invented. The flag leaves the address before the app reads it and holds for the rest of the
tab's life; `demo=read-ok` turns it off.

To show a failed read of the Legion setting, add `demo=settings-failed` the same way
(`/projects/<KEY>/issues?view=open&demo=settings-failed`). `GET /api/v1/settings/legion` then
answers 503 with an error text the demo makes up, so the page draws the project as one with no
daemon, with the "Legion setting read failed" alert and its Retry under the tabs, and the banner
names the failure. It holds for the rest of the tab's life; `demo=settings-ok` turns it off.

## Take the pictures

With the demo running:

```sh
cd packages/dispatch
TZ=UTC bun e2e/views-demo/shots.ts --base http://127.0.0.1:5190 --out "$TMPDIR/views-demo/shots" \
  --chromium <path to Chromium> [--before] [--no-daemon-project <KEY>] [--only <part>,…] \
  [--empty-work-time <KEY>] [--report <file name>]
```

Every page runs in the UTC time zone and the `en-US` language, and its clock stands at the
fixtures' read time (`web/src/demo/clock.ts`), so a run on another machine or another day draws
the same times. `report-<mode>.json` writes down the Chromium version and every font the browser
drew the pictures' text in, with its glyph count, so two runs can be compared.

Without `--before` it takes the design's pictures, each named by what it shows. At 1440 x 900:

- the two built-in views: `view-open-1440` and `view-done-1440`;
- `view-open-last-column-1440`: Open scrolled sideways to its last column, the pinned columns
  still in place;
- `view-open-1024`: Open in a 1024 x 768 window, where the sidebar folds away and the table
  scrolls sideways;
- `view-open-in-progress-1440`: Open with `status=in_progress`;
- `table-held-live-change-1440` and `table-held-shown-1440`: Open sorted by Priority after
  another reader sets its second row to P3 (a PATCH to the stub from outside the page's own
  write store, which reaches the page as a live event): the row's Priority cell reads P3 in
  place, the first rows stay where they were, and a small pill floating over the table's header
  offers "Show N changes"; then the same table after the press, the row gone from the first
  page;
- `old-queue-link-1440`: an older `view=queue` link, which opens Open with `f.queue=Queued`;
- `done-closed-filter-1440`: Done with one week picked in the Closed column's date-range
  filter, from the 3rd of November to the 9th, both days included, and the filter opened again
  to show it;
- `done-time-per-phase-hours-1440`: Done with Time per phase in Hours; Done scrolls sideways at
  1440, and Time per phase stands on its first screen;
- `done-time-per-phase-tooltip-1440`: Done with the pointer on the first row's Time per phase
  cell and its tooltip open, inside the window;
- `done-work-time-tooltip-focus-1440`: Done with Work time's header reached by Tab from the
  filter before it, its tooltip open on the keyboard focus alone, inside the window;
- `done-work-time-empty-1440`: Done narrowed to the day a done issue with Legion facts and no
  measured work closed (the newest closed of them, or the one `--empty-work-time` names),
  whose Work time cell is empty beside rows that draw one;
- `board-open-1440` and `board-open-legion-phase-1440`: Open on the Board by Status, and by
  Legion phase (`group=legion_phase`);
- `legion-choice-board-1440`: a browser that kept the old Legion view as its
  `project.issue-view` choice, which opens Open on its Board by Legion phase;
- `board-card-legion-facts-1440`: one card of the Board by Legion phase alone, at twice the
  scale, with its Time in Legion phase, Work time and Workers;
- `group-by-menu-1440` and `board-group-by-menu-1440`: Open with the Group by menu open, in the
  table and on the Board, where it offers Status and Legion phase;
- `board-held-live-change-1440` and `board-held-shown-1440`: Open's Board after another reader
  moves the first Todo card to Backlog: the card stays first in Todo, the two column counts move
  at once, and the pill floating over the column headers offers "Show 1 change"; then the same
  Board after the press, the card in Backlog;
- `board-own-drag-applies-1440`: the reader's own drag of the first Todo card onto Backlog,
  which moves it at once, with no Show changes pill;
- `save-view-popover-1440` and `save-view-saved-tab-1440`: Open narrowed to the triage issues,
  the Save view panel with "Triage" typed, and then the saved tab it adds after Done;
- `columns-menu-1440`: the Columns menu;
- `filter-open-asks-1440`: Open asks' filter open (With open asks, No open asks);
- `filter-time-in-legion-phase-1440` and `filter-why-it-waits-1440`: Time in Legion phase, and
  Why it waits (Ready to start, A blocker, A person, None; Capacity to go on stands where a card
  waits for a slot), each ticked in the Columns menu with its filter open;
- `view-open-info-hint-open-1440`: the Legion daemon hint open, from its place in the control
  row;
- `project-settings-legion-panel-1440`: the project's settings page;
- `view-open-daemon-read-failed-1440` and `view-open-settings-failed-1440`: Open with the
  daemon's read failed (`demo=read-failed`) and with the Legion setting's read failed
  (`demo=settings-failed`);
- `no-daemon-view-open-1440` and `no-daemon-board-open-1440`: the table and the Board of a
  project with no Legion daemon (`--no-daemon-project`, GTM by default);
- `priorities-1440` and `priority-page-1440`: the whole of `/priorities` (the priorities and
  the two fault tables) and of the first priority's own page.

At 390 x 844: `view-open-390`, `board-open-legion-phase-390`, `done-time-per-phase-hours-390`
and `priorities-390`, the last one the whole page.

A whole-page picture draws the banner at the page's foot, under everything the page drew. The
card's close-up has no banner: it is the card alone.

`--before` takes the page before the design, on a checkout of its base: the List, the Board, the
Legion view and the phone List. The base has no priority pages, so `before-priorities-1440` and
`before-priority-page-1440` are taken on a checkout of main with the same stub and fixtures:

```sh
TZ=UTC bun e2e/views-demo/shots.ts --before --only before-priorit --report report-before-main.json \
  --base http://127.0.0.1:<main's demo port> --out "$TMPDIR/views-demo/shots" --chromium <path>
```

`--only` takes the pictures whose names hold one of its comma-separated parts, and `--report`
names the report, `report-<mode>.json` by default. A picture is taken only once its page has
drawn its rows (a table row, a Board card or a Legion view card) or its own element; a page that
does not draw them in 30 s fails its picture. The design's Issues page is also waited on for its
view tabs (`[data-view-tab]`), which the page before the design has none of, so the design's
pictures taken against that page fail at the first one. The run fails if a picture failed, the
stub left a request unanswered, the page threw, the phone banner took more than two lines, or a
check of what the page drew failed:

- a header cut on Open (at 1440, at 1024 and scrolled to its last column) or on Done, or Open's
  last column not drawn whole;
- the bar's legend drawing a scroll bar;
- the old `view=queue` link not opening Open with `f.queue=Queued` on as many rows as the
  daemon's queue holds;
- the count the Done tab reads for the picked week not being the stub's count of the issues
  closed from the 3rd through the 9th, or the week the filter writes;
- the Time per phase tooltip naming no phase, or the table scrolled sideways under it;
- a held table moving its first rows, drawing the changed row's cell unchanged or no
  Show changes pill, or, after the press, keeping the changed row on the page or the pill;
- a held Board moving its first Todo cards or its moved card, not moving its column counts at
  once, or offering anything but "Show 1 change", or, after the press, keeping the card in Todo
  or the pill;
- the pill standing anywhere but over the table's header or the Board's column headers,
  centred on the list and under a third of its width, or in the pager row, or more than one
  drawn;
- the header or the first row or card moving between the moment before the change, the moment
  the pill shows and the moment after the press, in the table or on the Board;
- the reader's own drag not moving its card at once, moving the column counts wrongly, or
  drawing the pill, in the demo and in the single file;
- Work time's tooltip not opening on keyboard focus, holding more than one sentence, or not
  naming Legion queue hours;
- a tooltip standing partly outside the window;
- the done issue with no measured work drawing anything in its Work time cell;
- Created by or Open asks not whole on Open's first screen at 1440, on INFRA or on the project
  with no daemon;
- Time per phase not whole on Done's first screen at 1440, in Share or in Hours;
- a Board card reading "Workers None" where no worker runs, or drawing its claimer's name in
  under 24 px, on any of the four Boards (Open by Status, Open by Legion phase, the kept Legion
  choice, and the Board of the project with no daemon);
- the kept `legion` choice not opening Open's Board by Legion phase;
- the Share/Hours switch standing outside the window on a phone;
- the card not reading Time in Legion phase, Work time and Workers, or reading an old name;
- the Board's Group by menu not offering Status and Legion phase, or the Board not by Legion
  phase under `group=legion_phase`;
- the Columns menu missing a Legion column's name, holding an old one, or scrolling;
- Open asks', Time in Legion phase's or Why it waits' filter missing one of its choices, Why it
  waits offering Capacity to go on with no card waiting for a slot (or not offering it with
  one), or any of its choices holding the word queue or slot;
- the saved tab not standing open, or a failure flag left in the address.

The report in the output lists all of it.

## Build the single file

```sh
cd packages/dispatch
bun e2e/views-demo/single-file.ts --fixtures "$TMPDIR/views-demo/fixtures" \
  --out "$TMPDIR/views-demo/<name>.html"
TZ=UTC bun e2e/views-demo/shots.ts --file --base "file://$TMPDIR/views-demo/<name>.html" \
  --out "$TMPDIR/views-demo/shots" --chromium <path to Chromium>
```

The first command makes one Vite build (`DISPATCH_DEMO_SINGLE_FILE=1`: one script with every
asset inlined, `inlineDynamicImports`). Then it writes the script, the stylesheet, the icon and
the fixtures into the page. The page opens from the disk with no network. No service worker runs
at a `file:` address, so the stub replaces `fetch` and `WebSocket` in the page. The router reads
the address's fragment, the one part of a `file:` address a page may change.

The second command opens the file with the network off and checks that:

- the file opens on the Issues tab, and the Done tab opens Done;
- Open on the Board draws cards;
- a drag on the Board sets a status, and the card moves at once, with no Show changes pill;
- Save view adds a tab, and its delete takes the tab away, also after a reload;
- the Columns menu shows a column it ticks;
- the Closed filter's 7 days on Done lists the done issues closed in the last 7 days, counted
  on the Done tab, since no pager draws a total;
- the priorities page lists every priority the stub answers, and a press on one opens its own
  page with its issue table.
