# Dispatch design system

How a page of Dispatch is built from the shared primitives in this folder. Read it before you
style anything. `features/settings/` is the reference page: forms, tables, section heads and
every button variant, built only from what is listed here.

The aim: calm, tight and consistent. Every control is the same height, every card the same
radius, every page starts at the same left edge. If you are about to write a height, a radius, a
border colour or a hover on a `<button>`, `<input>` or a pill, stop and use the primitive.

## Which primitive for what

| You need | Use | From |
| --- | --- | --- |
| A button | `<Button variant size>` | `Button.tsx` |
| A link or `summary` that looks like a button | `buttonClassName({ variant, size })` | `Button.tsx` |
| A button that says why it is refused | `<RefusableButton>` | `RefusableButton.tsx` |
| A text input, select, textarea | `<Input>`, `<Select>`, `<Textarea>` | `Field.tsx` |
| A label above a control, hint and error below | `<Field label hint error>` | `Field.tsx` |
| The head of a page | `<PageHeader title description actions eyebrow>` | `Layout.tsx` |
| The head of a section | `<SectionHeader title description action>` | `Layout.tsx` |
| A bordered surface | `<Card>` or `cardClassName()` | `Layout.tsx` |
| A static word or count on a row | `<Badge tone>`, `<CountBadge>`, `<AttentionBadge>` | `Badge.tsx` |
| A label, a status, an approval state | `<Pill>`, `<LabelPill>`, `<StatusPill>`, `approvalPillClassName` | `Pill.tsx` |
| A pill the reader presses (filter, toggle, remove) | `<Chip>` | `Chip.tsx` |
| A plain table | `<Table head>`, `<Th>`, `<Tr>`, `<Td>` | `Table.tsx` |
| A sortable, filterable, virtualized table | `<DataTable>` | `DataTable.tsx` |
| Tabs | `<Tabs>` | `Tabs.tsx` |
| Nothing to show | `<EmptyState>` | `EmptyState.tsx` |
| A searchable multi-pick | `<MultiSelect>` | `MultiSelect.tsx` |
| A floating panel anchored to a control | `<AnchoredPanel className={popoverPanel}>` | `AnchoredPanel.tsx`, `theme/classes.ts` |
| Copy a value, pin a thing, fold rows | `<CopyButton>`, `<PinButton>`, `<DisclosureToggle>` | their files |
| Page next to more rows | `<ShowMore>` | `ShowMore.tsx` |

Colour composites that go with them, all in `theme/classes.ts`:

| Role | Composite |
| --- | --- |
| A row of a list or menu under the pointer | `rowHoverBg` |
| The current or selected row (one step stronger than hover; primary or secondary text on it, not muted) | `rowSelectedBg` |
| A link that is chrome (issue key, breadcrumb, "Open") | `linkChrome` |
| A link inside a sentence | `linkInline` |
| A floating panel's whole box | `popoverPanel` |
| A key hint (`<kbd>`) | `border ${kbdKey}` |
| Text | `textPrimaryOn…`, `textSecondaryOn…`, `textMutedOn…` |
| Rules | `borderDefault` (dividers, cards), `borderStrong` (controls) |

## Buttons

```tsx
<Button variant="primary" type="submit">Save</Button>
<Button>Cancel</Button>                               {/* secondary is the default */}
<Button variant="ghost" size="sm">Clear filters</Button>
<Button variant="danger">Revoke</Button>
<Button variant="ghost-muted" size="sm" iconOnly aria-label="Close"><CloseIcon /></Button>
<Button variant="primary" loading>Saving…</Button>
<Button aria-pressed={on} onClick={toggle}>Board</Button>   {/* a toggle: pressed fill */}
<Link className={buttonClassName({ variant: "primary" })} to="/">Back to inbox</Link>
```

- `primary`: the filled accent. **At most one per view.** The thing the page is for.
- `secondary` (default): surface and a 1px rule. Every other action that stands alone.
- `ghost`: no rule, no fill until hover. Actions inside a row, a toolbar or a card head, and
  every icon-only button. `ghost-muted` for an icon that should not compete with text.
- `danger`: destroys or revokes. Secondary's shape, danger text.
- Sizes: `md` is 32 px (`h-8`), `sm` is 28 px (`h-7`) for a compact row or toolbar. Do not mix
  sizes in one row. `iconOnly` makes it square and needs an `aria-label`.
- `className` is for placement only: a margin, `w-full`, a grid cell. Never a colour, height,
  radius, padding or font.
- `disabled` and `aria-disabled` both draw the cannot-press look. `loading` disables and draws a
  spinner.
- A button with text reads as a verb: "Save", "New project". No trailing punctuation.

Don't: `<button className="rounded-lg border px-3 py-2 …">`, `min-h-11`, a grey filled button
(`surfaceMutedBg`) beside a bordered input, a hover that changes the border to the accent.

## Fields

```tsx
const id = useId();
<Field label="Name" htmlFor={`${id}-name`} hint="Shown in the sidebar." error={refusal}>
  <Input id={`${id}-name`} value={name} onChange={…} />
</Field>
<Field label="Default assignee" htmlFor={`${id}-assignee`}>
  <Select id={`${id}-assignee`} value={v} onChange={…}>…</Select>
</Field>
<Field label="Production jobs" htmlFor={`${id}-jobs`} hint="One per line.">
  <Textarea id={`${id}-jobs`} mono rows={3} />
</Field>
```

- `Input`, `Select` and `Textarea` share a button's height (32 px, `size="sm"` 28 px), radius,
  rule and focus ring, so a field and a button on one row line up with `items-end` or
  `items-center` and nothing else.
- `surface="recessed"` when the field sits inside a `Card`; the default is a field on the page.
- `mono` for identifiers (a repository, a path, a job name).
- `Select` is the native select with one chevron of its own; `className` sets the wrapper's
  width (it is `w-full` by default).
- Labels go **above** the control, `text-xs` medium. Use `Field`, whose `htmlFor` is the
  control's `id`; for a `legend` or a label
  that is not a `Field`, `fieldLabelClassName` and `fieldHintClassName`.
- A form's fields are 16 px apart (`gap-4`); label, control and hint are 6 px apart (that is
  `Field`'s own). The submit button sits at the form's left edge under the fields, or at the end
  of a single-row form, bottom-aligned with the controls.
- A checkbox or radio keeps the native control: `<label className="flex items-center gap-2
  text-sm">` with the input first.

## Page layout

The shell (`app.tsx`) gives `<main>` the gutter: 32 px at the sides and 24 px above on desktop,
16 px on a phone. A page adds **no outer padding or margin of its own**.

```tsx
<section className="max-w-5xl">        {/* a reading page; a table page takes the full width */}
  <PageHeader title="Machines" description="Every machine a live session names." actions={…} />
  <div className="space-y-8">           {/* sections 32 px apart */}
    <section aria-labelledby="x-heading">
      <SectionHeader id="x-heading" title="Projects" description="…" action={…} />
      …content…
    </section>
  </div>
</section>
```

- Page title 22 px semibold (`PageHeader`), section heading 15 px semibold (`SectionHeader`),
  body `text-sm`, meta and labels `text-xs` muted. No other sizes. Do not mix sizes in one row.
- `PageHeader` has 24 px under it. `SectionHeader` has 12 px under it. Sections are 32 px apart
  (`space-y-8`), blocks inside a section 16 px (`space-y-4`), related rows 8 px (`space-y-2`),
  items inside a row 8 px (`gap-2`). All spacing is a multiple of 4 px.
- A back link or breadcrumb is the header's `eyebrow`: `<Link className={linkChrome}>`.
- A page's toolbar (search box, filters, view switch) is one row: `flex flex-wrap items-center
  gap-2`, every control the same size, the search `Input` first, the view switch at the right
  (`ml-auto`).
- Every block on the page shares the page's left edge: the title, the description, the toolbar,
  the table's first column, the form's first field.

## Cards, rows and tables

- `Card`: `rounded-lg`, 1px `borderDefault`, the surface fill, 16 px padding (`padding="sm"` 12,
  `"none"` for a card whose rows run edge to edge). A card is one object (an agent, a broadcast,
  a machine) or one panel. Do not wrap a whole section in a card, and do not nest cards.
- A list of cards is `space-y-2`. A list of rows inside one card is divided by
  `divide-y ${borderDefault}`-style rules, each row `px-4 py-3`.
- Hover: a row or card that is a link or opens something takes `rowHoverBg` (a faint tint).
  **Never a border colour change** (`cardHoverBorder`, `controlHoverBorder` and
  `secondaryButtonHoverBorder` are the old way; do not add new uses, and remove them where you
  migrate).
- `Table`: no box, first column flush with the page's left edge, header `text-xs` muted, rows
  40 px. Give a cell its text tone through `Td`'s `className`; row actions are `ghost` `sm`
  buttons in the last cell, right-aligned (`className="text-right"`).
- An empty list is one `EmptyState`, in the place the list would be.

## Badges, pills, chips

One shape family: 20 px tall, `text-xs` medium, 6 px inset, 4 px corners (`rounded`), centred on
the line. So a count, a label and a status in one row are the same height without any tweak.

- `Badge` / `CountBadge`: a static mark. Tone means state: `danger` blocks, `warning` needs
  attention, `success` is well, `info` is the accent, `neutral` (default) says nothing,
  `outline` is the quiet one.
- `Pill`, `LabelPill`, `StatusPill`: labels and statuses (sentence case, never uppercase).
- `Chip`: the one the reader presses, 24 px. `selected` for a toggle, `removable` for a filter.
- Never put a badge flush against text: the row's `gap-2` separates them. A count at the end of
  a row is right-aligned and `tabular-nums` (that is `CountBadge`).
- Colour means state. A row of five coloured pills says nothing; default to `neutral`.

## Links

- A link in running text is underlined (`linkInline`).
- A link that is chrome is the link colour with **no underline until hover** (`linkChrome`): an
  issue key in a row, "Open", a breadcrumb, a name that leads to its page.
- A title that is a link (a card's heading) is the text colour, not the link colour, and
  underlines on hover: `hover:underline underline-offset-2`.
- Navigation items are never underlined.

## Focus, hover, disabled

- The focus ring is global (`styles.css`): 2 px of the focus accent, 2 px off a button or link,
  on the rule of a field. Do not write `outline-none` or a ring of your own unless the control
  draws its focus somewhere else on purpose.
- Buttons, selects, summaries and checkbox labels show the hand cursor; that is global too.
- Disabled is the variant's own look. Do not add `opacity-50` to a `Button`.

## Touch targets

Below 1280 px `styles.css` lifts every `button`, `a`, `input`, `select` and `textarea` to a
44 px minimum box, whatever the classes say. So write the desktop size (`h-8`, `h-7`, `size-7`)
and nothing else: **no `min-h-11`, no `md:min-h-8`** in a component. Check each page at 390 px:
a row of several controls becomes 44 px tall there and may need `flex-wrap`.

## Dark mode

Every colour comes from a composite in `theme/classes.ts`, which carries both schemes. A raw
`bg-`/`text-`/`border-` colour outside `theme/` fails `no-raw-colors.test.ts`. Look at every
change in both schemes: the canvas is `slate-950` in dark, a card `slate-900`, a field on the
page `slate-900` and a field inside a card `slate-950` (`surface="recessed"`).

## The new issue page is separate

Controls drawn inside `NoTooltips` (the new issue page, behind the
Settings preview switch) keep that page's own approved palette and sizes. The shared components
branch on `useTooltips()` for it; leave those branches as they are.

## Do / don't

| Do | Don't |
| --- | --- |
| `<Button>` | a hand-rolled `<button className="rounded… border… px… py…">` |
| `h-8` controls in one row | 44 px buttons beside 36 px inputs on desktop |
| one `primary` per view | two filled buttons side by side |
| `rowHoverBg` on a clickable row | `hover:border-sky-…` on a card |
| `linkChrome` for an issue key | `underline` on every link |
| `text-xs` muted for meta | 11 px, 13 px, `text-[10px]`, uppercase tracking labels |
| `rounded-md` controls, `rounded-lg` cards, `rounded` badges | `rounded-xl`, `rounded-2xl`, `rounded-full` pills |
| `space-y-8` between sections | `mt-6` here, `mt-10` there |
| `<Field label>` above the control | a label beside the control, or a placeholder as the label |
| `<EmptyState>` | a bare grey sentence floating in the page |
| sentence-case labels | UPPERCASE section labels |

## Tests that pin class strings

`features/issue-page/shared-controls.options.test.tsx` ("the default shapes keep main's class
strings", and `RECORDED_CLAIMS`) pins the exact class string of many controls, shared and
feature-owned (`ApprovalSteps`, `AgentName`, `ClaimChip`, `GitHubLink`, ...). Restyling any of
them fails it. That is a presentation assertion: update the pinned rows for what you changed on
purpose, and nothing else. From `packages/dispatch`:

```bash
python3 /tmp/dispatch-ui/update-class-snapshot.py   # rewrites the rows the failure names
git diff web/src/features/issue-page/shared-controls.options.test.tsx   # read every row
```

The foundation branch already updated the rows of the shared components; after
`git merge ui-foundation` only your own controls' rows should differ.
