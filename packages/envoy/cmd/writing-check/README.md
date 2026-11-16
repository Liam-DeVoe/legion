# writing-check

`writing-check` takes the measurements of Dispatch's writing check (INFRA-3231) under the method
of INFRA-3189. It is a development tool. No image and no release ships it.

Every file it writes holds Dispatch text or model replies. Write them under `$TMPDIR`, never into
a repository.

```sh
go run ./cmd/writing-check probe-model --kind <kind> --endpoint <url> --deployment <id> [flags]
go run ./cmd/writing-check fetch --after <event id> --through <event id> --out <cases.jsonl> [--resume]
go run ./cmd/writing-check group --in <cases.jsonl> --out <groups.jsonl>
go run ./cmd/writing-check readers --in <cases.jsonl> --log-after <event id> --log-through <event id> --out <cases.jsonl> [--state <file>]
go run ./cmd/writing-check rule-cost --in <cases.jsonl> --seed N --out <dir> [--writes 500] [--min-per-surface 30] [--plant <rule id>]
go run ./cmd/writing-check split --in <grouped.jsonl> --cut-a <event id> --cut-b <event id> --out <dir>
go run ./cmd/writing-check plant --in <manifest.jsonl> --corpus <cases.jsonl> --seed N --out <dir> [--per-shape 40] [--nolist 40] [--controls]
go run ./cmd/writing-check label --in <cases.jsonl> --definition <file> --out <labels.jsonl> --endpoint <url> --key-file <file> [flags]
go run ./cmd/writing-check validate-labeller --labels <labels.jsonl> --planted <manifest.jsonl> --seed N --out <report.md>
go run ./cmd/writing-check recall --labels <labels.jsonl> [--planted <manifest.jsonl>] [--heldout-seed N --heldout-share 0.5] --out <dir>
go run ./cmd/writing-check digest [--root <module root>]
go run ./cmd/writing-check score --labels <labels.jsonl> [--planted <manifest.jsonl>] --stratum real|planted --out <dir> [--final-test --set-digest <sha> --seed N --exposure-log <file>] <reader flags>
go run ./cmd/writing-check reproduce --outputs <outputs.jsonl> --labels <labels.jsonl> [--planted <manifest.jsonl>] --figures <figures.json> [--bootstrap-seed N]
go run ./cmd/writing-check regress --cases <cases.jsonl> --repeat-bound <share> <reader flags>
go run ./cmd/writing-check reading --in <cases.jsonl> --after <event id> --through <event id> [--exclude <manifest>]... --out <dir> <reader flags>
go run ./cmd/writing-check validate --description <scanner.yaml> --exposure-log <file> [--manifests <dir>]
go run ./cmd/writing-check dataset fetch --end <RFC 3339 time> --out <snapshot.jsonl.gz> [--labelled <sets tarball>]
go run ./cmd/writing-check dataset classify --snapshot <snapshot.jsonl.gz> --out <verdicts.jsonl> [--model <id>]
go run ./cmd/writing-check dataset pack --snapshot <file> --verdicts <file> --seed N [--confirmed <file> --confirmed-seed N] --out <bundle.tar.gz>
go run ./cmd/writing-check dataset build (--snapshot <file> --verdicts <file> --seed N [--confirmed <file> --confirmed-seed N] | --from <bundle>) --out <dir>
go run ./cmd/writing-check dataset sessions --manifest <export manifest.json> --cases <build dir> --out <dir>
go run ./cmd/writing-check dataset owner-texts (--snapshot <file> | --from <bundle>) --out <dir>
go run ./cmd/writing-check dataset named --doc <issue>/<document>@<version>[,<version>...] [--doc ...] --out <dir>
cmd/writing-check/export-sessions.sh --sessions <sessions.tsv> --exclude <exclude.txt> --out <dir>
go run ./cmd/writing-check harness probe [--calls 20] [--out <file>] <model flags>
go run ./cmd/writing-check harness score --cases <build dir> --sessions <dir> --out <dir> [--change <change> [--model <id> | --models <id>,<id>...]] [--part development|held_out] [--sample N --sample-seed S] [--grader <model>] [--grader-inputs final|full|trimmed[,j=mode]...] [--readers-from <dir>] [--grades-from <dir>[,<dir>...]] [--vocabulary <owner-texts.jsonl.gz>] [--judgements j,...] [--repeatability <file>] <model flags>
go run ./cmd/writing-check harness grader-test --planted <file> --cases <build dir> --sessions <dir> --out <dir> [--grader <model>] [--grader-inputs ...] <model flags>
go run ./cmd/writing-check harness compare --a <run dir> --b <run dir> --out <repeatability.json>
go run ./cmd/writing-check harness phrases-labels --labels <labeller round dir> --out <dir> <model flags>
go run ./cmd/writing-check harness asked-words --run <run dir> --cases <build dir> --out <file>
go run ./cmd/writing-check harness local --out <dir> [--database <name> --keep] [--setting NAME=VALUE]... [--pass-through NAME,...]
go run ./cmd/writing-check harness load --local <local.json> --cases <build dir> --verdicts <file> --out <dir> [--limit N]
```

Run it from `packages/envoy`.

## probe-model

`probe-model` times the model reader's calls against one model endpoint. Each call goes through
the real reader (`writingcheck/reader`: `reader.New`, then `Read`). The candidates come from the
real code stage (`writingcheck/candidates`) over one body. So the prompt, the reply schema and the
bounds are the bundle's.

| Flag | Meaning |
| --- | --- |
| `--kind` | `openai-chat` for an Azure AI Foundry deployment, `anthropic-messages` for a model gateway. |
| `--endpoint` | The endpoint's base URL. |
| `--deployment` | The model id, or the Azure deployment name. |
| `--api-version` | The `api-version` query parameter, for an Azure deployment. |
| `--azure-tenant`, `--azure-client-id`, `--federated-token-file`, `--scope` | Keyless Azure sign-in through `embed.NewEntraTokenSource`, as the answerer signs in. `--scope` defaults to `embed.DefaultEntraScope`. |
| `--key-file` | A file that holds the endpoint's key. `openai-chat` sends it as a bearer token, `anthropic-messages` as `X-Api-Key`. The command never prints it. |
| `--calls` | The number of calls (default 20). |
| `--deadline` | The reader's time bound for one call (default 0: no bound). |
| `--bodies` | A JSONL file of `{"text": ...}` bodies. Without it, the command uses a built-in set of synthetic sentences. A body that holds no candidate is not sent. |
| `--out` | A JSONL file for the per-call records. |
| `--no-effort` | Send no `reasoning_effort` (`openai-chat` only), for a deployment that refuses the parameter. Without it, the reader asks for effort `off`, which Chat Completions receives as `reasoning_effort: none`. |

Give either `--key-file` or the Azure sign-in flags.

Each record holds the call's start time, its latency in milliseconds, the reader's outcome (`read`
for a call that read every candidate, else `reader_timeout`, `reader_error`, `reader_malformed`
and the rest), the model id the provider said served the call (empty when no reply came), the
error text, and the number of candidates sent.

The summary prints p50, p95 and the maximum latency (nearest rank), the share of calls that failed
(or, with a `--deadline`, went over it), the count of parse failures (`reader_malformed`), the count
of each outcome and of each served model id, and the window the calls ran in.

## fetch

`fetch` pages the event log (`GET /api/v1/events/log`, 500 events a page, oldest first) from the
event after `--after` through `--through`. It writes one case for each prose field of each write
that the actor rule checks.

The actor rule is `writingcheck.Checks`, the function the server calls:

- A `session` actor is checked, except a daemon session (`legion-daemon:<PROJECT>`).
- A `user` actor is checked only when its act names a session (`act.session`) that is not a
  daemon session.
- A `system` actor, and a person who wrote for themselves, are not checked.

`fetch` counts the writes the rule exempts, and prints them on stderr by exemption
(`exempt_person`, `exempt_daemon`, `exempt_system`) and by event type. It reads nothing else for
an exempted write.

Each case holds these fields:

| Field | Meaning |
| --- | --- |
| `event_id`, `event_type`, `time` | The event, and when it was written. |
| `project`, `issue` | Where it was written; `issue` is empty for a project document or a direct message. |
| `root_issue` | The top of the issue's ancestors, or the issue itself when it has no parent. An issue event names the ancestors itself. For other events, `fetch` reads each issue's parent once (`GET /api/v1/issues/{key}`) and keeps it. |
| `document`, `document_name` | The document's artifact id and name, when the text is on a document. |
| `source_id` | The comment, message, ask or result id; the broadcast id; or `<artifact id>@<version>`. |
| `surface` | The kind of write, from the table below. |
| `field` | The JSON path of the text in the payload, such as `body`, `options[1].label` or `resolution.reason`. |
| `actor_kind`, `actor_id`, `act_session` | The event's actor, and the session its act names. |
| `text`, `sha256` | The text, and the SHA-256 of its UTF-8 bytes. |
| `cut` | `true` when Dispatch would not serve the whole text (deleted since, or not readable with this credential), so the case holds the part the event carried. |

An event payload cuts a comment's or a message's body at 32,000 UTF-16 units
(`body_truncated`). `fetch` then reads the whole body by its id.

`fetch` makes at most 20 requests a second. It retries a 5xx, a 429 or a network error up to 5
times, waiting 0.5s, 1s, 2s, 4s and 8s.

At the end it prints the count of cases by surface on stdout. On stderr it prints the exempted
writes, and by surface the fields Dispatch would not serve whole.

`--resume` continues a cases file that an earlier run did not finish. It drops a line cut short,
and the lines of the last event the file holds, because that event's cases may be incomplete.
Then it reads again from that event. Each event's cases are written in one write.

`DISPATCH_URL` names the Dispatch server. `DISPATCH_TOKEN_FILE` names the file that holds the
bearer token. The command reads the token file on every request and never prints the token.

### Prose surfaces

Each row names a surface, the event that carries it, the route that writes it, and the reads
that enumerate it. Every surface is enumerated by `GET /api/v1/events/log`; the last column names
the extra read, where there is one.

| Surface | Event and field | Route that writes it | Extra read |
| --- | --- | --- | --- |
| `comment` | `comment.created` `body` | `POST /api/v1/issues/{key}/comments`, `POST /api/v1/artifacts/{id}/comments` | `GET /api/v1/comments/{id}` when the body is cut |
| `comment_suggestion` | `comment.created` `suggestion.replace_with` | the same routes, with a suggestion (its `body` is the reason, a `comment` case) | none |
| `comment_edit` | `comment.edited` `body` | `PATCH /api/v1/comments/{id}` | `GET /api/v1/comments/{id}` when the body is cut |
| `comment_delivery_reply` | `comment.answered` `body` | `POST /api/v1/comments/{id}/reply` | `GET /api/v1/comments/{id}` when the body is cut |
| `message` | `message.created` `body`, with no target and no broadcast | `POST /api/v1/issues/{key}/messages` | `GET /api/v1/messages/{id}` when the body is cut |
| `send_to_agent` | `message.created` `body`, with a `target` | `POST /api/v1/issues/{key}/messages` with a target, `POST /api/v1/agents/{session_id}/messages` | as `message` |
| `broadcast` | `message.created` `body`, with a `broadcast_id` | `POST /api/v1/broadcasts`; one case for each broadcast, not one for each recipient's copy | as `message` |
| `delivery_reply` | `message.answered` `body` | `POST /api/v1/messages/{id}/reply`, and a message posted with `in_reply_to` | as `message` |
| `ask_question` | `ask.opened` and `ask.edited` `question` | `POST /api/v1/issues/{key}/asks`, `POST /api/v1/artifacts/{id}/asks`, `PATCH /api/v1/asks/{id}`, and a document's `:::ask` block | none |
| `ask_option` | `ask.opened` and `ask.edited` `options[i].label`, `options[i].description` | the same routes | none |
| `approval_summary` | `ask.opened` and `ask.edited` of an approval ask: the requester's summary after Dispatch's `Approve <documents>?` | `POST /api/v1/artifacts/{id}/approval-requests` | none |
| `ask_answer` | `ask.answered` `answer.text` | `POST /api/v1/asks/{id}/answer` | none |
| `ask_decline` | `ask.resolved` `resolution.reason` of kind `declined` | `POST /api/v1/asks/{id}/decline` | none |
| `ask_resolution` | `ask.resolved` `resolution.reason` of kind `retracted` or `resolved` | `POST /api/v1/asks/{id}/resolve` | none |
| `issue_title` | `issue.created` `title`; `issue.updated` `title` when `changed` names it | `POST /api/v1/issues`, `PATCH /api/v1/issues/{key}` | none |
| `document` | `artifact.created` of a `doc` artifact; `artifact.version` of a version with no MIME type (a document's) | `POST /api/v1/issues/{key}/artifacts`, `POST /api/v1/projects/{key}/artifacts`, the `.../edits` routes, live editing | `GET /api/v1/artifacts/{id}/versions/{n}`, and version n−1 when the run has not read it |
| `version_summary` | `artifact.created` and `artifact.version` `version.summary` | the `.../versions` routes (a named version), an upload with a summary | none |
| `review_reason` | `artifact.approved` and `artifact.changes_requested` `reason` | `POST /api/v1/artifacts/{id}/reviews`, an answer to an approval ask | none |
| `result_run`, `result_observed`, `result_negative_control` | `result.recorded`, by `result_id` | `POST /api/v1/issues/{key}/results` | `GET /api/v1/issues/{check_issue}/results`, kept for each issue and read again only for a result it does not hold yet (Dispatch has no read of one result by its id) |

These rules decide what a case holds:

- An `ask.edited` case holds only the wording that changed from the `previous` wording the event
  carries. An option's label or description is new when no option before the edit had that text.
- An approval ask's question is Dispatch's own words up to the `?` after the last document named.
  Its options are Dispatch's too. Only the requester's summary is a case.
- A document version's case holds only the blocks that the version before it does not hold. The
  text is split at blank lines. A fenced code block is one block, whatever blank lines it holds.
  A list is split into its items, and a table into its rows; a row is compared with its table's
  head (its first two lines). The blocks are compared as sets, so a block that moved is not new.
  The new blocks are joined by blank lines, and the new rows of one table are written under its
  head once.
- `message.answered` carries `reply_body`, the first 160 characters of the message it answers.
  That is the other writer's text, so it is not a case.

These events carry no prose that an agent writes, and `fetch` skips them:

- `handoff.recorded`;
- `artifact.human_edited` (a person's edits);
- `suggestion.accepted`, `suggestion.rejected`, `comment.resolved` and `comment.reopened`;
- an `ask.resolved` of kind `withdrawn` (Dispatch's own words when the issue closes);
- the delivery, claim, follower, state and anchor events.

## group

`group` gives each case a `group`. A set split must keep every case of one group on one side.
Two cases are in one group when a chain of these links joins them (union-find):

- the same root issue (the issue itself where the case names no root);
- the same document (every version of it, and the comments and asks on it);
- near copies: their sets of word 5-grams have a Jaccard similarity of 0.9 or more.

A word is a run of letters and digits, lower-cased. A text of fewer than five words is one
shingle of all its words. A shingle is kept as its 64-bit FNV-1a hash.

The near-copy join is exact: it finds every pair that comparing all pairs finds. It uses prefix
filtering (the all-pairs similarity join):

1. Cases with the same shingle set are joined at once, and only one of them goes on.
2. Each set's shingles are ordered rarest first, over all cases.
3. If two sets of sizes m ≤ n have a Jaccard similarity of t or more, they share at least ⌈t·n⌉
   shingles. So they share a shingle among the first n − ⌈t·n⌉ + 1 shingles of each, and the
   smaller set has at least ⌈t·n⌉ shingles.
4. Sets are taken shortest first. Each set looks up its prefix in an index of the prefixes taken
   before it, skips the candidates that are too short, and computes each other candidate's exact
   Jaccard similarity. Then its own prefix joins the index.

`TestNearCopiesMatchesEveryPairCompared` checks this join against a comparison of every pair, on
synthetic texts at thresholds 0.9, 0.75 and 0.5.

Group ids are `g1`, `g2` and so on, in the order of each group's first case. `group` prints the
count of cases and the count of groups.

## Writes and surface families

The subcommands below count by write. A write is one request: the cases that share an event id
and a source id. A planted copy of a write is a write of its own (its `copy` id tells it apart).

Each surface belongs to one family:

| Family | Surfaces |
| --- | --- |
| `comment` | `comment`, `comment_suggestion` |
| `comment_edit` | `comment_edit` |
| `message` | `message` |
| `delivery_reply` | `delivery_reply`, `comment_delivery_reply` |
| `send_to_agent` | `send_to_agent` |
| `broadcast` | `broadcast` |
| `ask` | `ask_question`, `ask_option` |
| `ask_answer` | `ask_answer` |
| `ask_resolution` | `ask_resolution`, `ask_decline` |
| `issue_title` | `issue_title` |
| `document` | `document` |
| `version_summary` | `version_summary` |
| `approval_summary` | `approval_summary` |
| `review_reason` | `review_reason` |
| `result` | `result_run`, `result_observed`, `result_negative_control` (one write) |

A write whose cases fall in two families is of the family listed first. So a document version
with its summary is a `document` write, and a version with only a summary is a
`version_summary` write.

A write's main prose field is the field a person reads first, in this order of surfaces:
`document`, `comment`, `comment_edit`, `comment_delivery_reply`, `message`, `delivery_reply`,
`send_to_agent`, `broadcast`, `ask_question`, `ask_answer`, `ask_decline`, `ask_resolution`,
`approval_summary`, `review_reason`, `result_observed`, `result_run`,
`result_negative_control`, `version_summary`, `comment_suggestion`, `ask_option`,
`issue_title`. Among several fields of one surface, the longest is the main field.

A planted sentence goes in as its own paragraph at a paragraph start of the main field. A
paragraph start is the start of the text (after front matter), the start of a line that follows
a blank line, opens with no white space and is outside a fenced code block, and the end of a text
that does not end inside a fence.

## readers

`readers` gives each case the person who read its write when the write was made: `reader` (a
login, or `""` when no person reads it) and `reader_rule`. The writing check reads only a write
that a person reads, so the measurements draw only from cases whose `reader` is not empty.

The rule is the server's: `writingCheckReader` with its table `writingCheckReaderRules`
(`internal/dispatch/api/writing_check_reader.go`). `reader_rule` is the name the table gives the
rule, and `TestEveryReaderRuleNameIsTheServers` holds the names to that file. A rule reads records:
an issue's assignee, a project's default assignee, the person an ask was handed to (`asked_of`),
the author of a comment, message or ask, and the person who opened a direct conversation.
`readers` reads each record as it stood just before the write. An issue's create is read as it
stood after the create, because the server decides that reader from the request.

| Event of the case | Route that wrote it | `reader_rule` | Reader |
| --- | --- | --- | --- |
| `issue.created` | `POST /api/v1/issues` | `new_issue` | the assignee the issue was created with; with none, the person the credential acts for, the parent's assignee, then the project's default |
| `issue.updated` | `PATCH /api/v1/issues/{key}` | `issue` | the issue's assignee, else the project's default |
| `comment.created` | `POST .../comments` | `comment` | the author of the comment (`answers`, else `reply_to`) or the ask (`ask_id`) it answers, when a person wrote it; else the reader of its issue or document |
| `comment.answered` | `POST /api/v1/comments/{id}/reply` | `comment_reply` | the author of the comment it answers when a person wrote it; else that comment's issue or document reader |
| `comment.edited` | `PATCH /api/v1/comments/{id}` | `comment_edit` | as `comment`, from the stored comment |
| `message.*` with a `broadcast_id` | `POST /api/v1/broadcasts` | `none` | nobody |
| `message.*` on no issue, not a session's reply in its own conversation | `POST /api/v1/agents/{session_id}/messages` | `none` | nobody |
| `message.answered` whose target is its author's own session | `POST /api/v1/messages/{id}/reply` | `message_reply` | the author of the message it answers when a person wrote it; else its issue's reader; else, on no issue, the person who opened the thread, and nobody when a session did |
| `message.*` on an issue | `POST /api/v1/issues/{key}/messages` | `message` | the author of the message it answers when a person wrote it; else the issue's reader |
| `ask.opened` | `POST .../asks` | `issue`, or `document` for an ask on a document, an approval request or a document's `:::ask` block | the reader of the issue or the document |
| `ask.edited` | `PATCH /api/v1/asks/{id}` | `ask` (`document` for an approval request or a block ask) | the person the ask was handed to, else its issue's or document's reader |
| `ask.answered`, `ask.resolved` | `POST /api/v1/asks/{id}/answer`, `/decline`, `/resolve` | `ask_reply` | the ask's author when a person wrote it; else its issue's or document's reader |
| `artifact.created` | the upload routes | `issue`, `project` or `conversation`, by the document's owner | the issue's reader; the project's default; the person in the conversation |
| `artifact.version`, `artifact.approved`, `artifact.changes_requested` | `POST /api/v1/artifacts/{id}/...` | `document` | the reader of the document's owner, as above |
| `result.recorded` | `POST /api/v1/issues/{key}/results` | `issue` | the issue's reader |

An issue's reader is its assignee, else its project's default assignee, else nobody. The server
reads the owner of an answered comment or ask through the comment's or ask's own `issue_key`. A
comment or ask on a document has none, so the owner is the project's default even where the
document belongs to an issue. `readers` does the same.

`readers` replays the event log (`GET /api/v1/events/log`, 500 events a page, oldest first) from
the event after `--log-after` (0: the first event the server keeps) through `--log-through`. It
keeps:

- each issue's project, parent and assignee history, from every `issue.*` event that carries the
  issue (each carries the whole issue), and each issue's primary document, which the create makes
  with no `artifact.created` of its own;
- each project's default assignee history, from `settings.project_default_assignee.updated`;
- each ask's author, issue, document, approval document, whether a block holds it, and the
  history of `asked_of` (every `ask.*` event carries the whole ask);
- each comment's author, issue, document, the comment it answers and its ask;
- each message's author, issue, the message it answers, whether it is a broadcast copy, and
  whether it is a session's reply in its own conversation;
- each document's issue, project or session;
- for each session, the people who opened an issue-less thread to it, in order.

`--state` names a file for that state. When the file exists and was replayed from the same
`--log-after`, `readers` reads it and pages the log only after the last event it holds, so a
second run over the same window reads no log. A case outside the replayed window is refused.

Some history is not in the log:

- A project's default assignee changes only through `PUT /api/v1/projects/{key}/default-assignee`,
  which writes `settings.project_default_assignee.updated`. Before the first such event,
  `readers` uses the default that event replaced. A project the log records no default for has
  none. Migration `20261111210103_projects_default_assignee_not_null` fills every default
  without writing an event; a run over a window after that migration is deployed has to give
  those projects the filled default, which `readers` does not know.
- `asked_of` is in ask payloads from migration `20261109221325_asks_asked_of` on. An ask with no
  `asked_of` waits on its issue's assignee, which is what the server reads too.
- `answers` is in comment payloads from migration `20261105014808_comments_answers` on. A
  comment written before it carries only `reply_to`, the head of its thread, which `readers`
  takes as the comment it answers.

`readers` prints the cases and the writes with and without a person reader, in all, by family
(the families above; a write's cases count under its family) and by project; the writes by rule;
the writes by reader, with each reader's share of all writes; and the count of reads of a record
the replay does not hold (the server answers no reader for a record that does not exist).

## rule-cost

`rule-cost` measures the share of writes that each structure rule (`writingcheck/rules.All()`)
finds, and picks the rules to enable.

| Flag | Meaning |
| --- | --- |
| `--in` | The cases file. |
| `--seed` | The seed of the draw. |
| `--writes` | The least number of writes to draw (default 500). |
| `--min-per-surface` | The least number of writes to draw from each family (default 30). |
| `--out` | The directory for the output files. |
| `--plant` | A rule id, for a negative control (below). |

The draw is stratified by family:

1. Each family gives `--min-per-surface` writes, or all its writes when it has fewer.
2. The rest of `--writes` is shared out by each family's count of writes in the input (largest
   remainder). No family gives more writes than it has. When the minimums take more than
   `--writes`, the draw is larger than `--writes`.
3. Each family's writes are drawn uniformly by the seed, without replacement.
4. Each drawn write of a family carries the weight (writes of the family in the input) / (writes
   of the family drawn). So the weighted draw has each family's share of the input.

The command fails when the draw holds writes of fewer than two projects.

A write breaks a rule when a prose sentence of any of its fields (`prose.ProseSentences`) gives
a finding. For each rule, the table gives the pooled share (the weighted share of drawn writes
that break it) and, for each family, the count of drawn writes that break it.

The enable rule: take the rules whose pooled share is at most 5%, in rising order of share (ties
in the order of `rules.All()`). Enable each in turn while the union share, the weighted share of
writes that break any enabled rule, stays at most 10%. Stop at the first rule that would take the
union share over 10%. The table also lists `rules.LeftOut` with the reasons.

`--plant <rule id>` puts one sentence written for this command, which breaks that rule and no
other, into the main field of every drawn write at a paragraph start drawn by the seed. That rule
must then read 100%, and it is not enabled.

Output, in `--out`:

- `table.md`, which the command also prints, and `table.json`, with the same numbers;
- `manifest.jsonl`: one line for each drawn write, with `event_id`, `source_id`, `family`,
  `project` and `weight`;
- `manifest.sha256`: the SHA-256 of `manifest.jsonl`, in the format of `sha256sum`.

## split

`split` cuts a grouped cases file (the output of `group`) into the three sets of the method:
development, calibration and final pool. Each group goes whole to the set of its first case (its
lowest event id):

- `development`: the first case is before `--cut-a`;
- `calibration`: the first case is at `--cut-a` or after it, and before `--cut-b`;
- `final-pool`: the first case is at `--cut-b` or after it.

Output, in `--out`: one manifest for each set (`development.jsonl`, `calibration.jsonl`,
`final-pool.jsonl`) and `counts.json`. Each manifest line is one case: `id`
(`<event id>/<source id>/<field>`), `event_id`, `source_id`, `field`, `group`, and the empty label
slot `label`, `label_source` and `label_revision` (all `null`). `counts.json` holds the cuts, the
count of cases, writes and groups of each set, and each manifest's SHA-256. The command prints
the counts.

The command fails on a case with no group, on a group in two sets and on a write in two sets.

## plant

`plant` copies real writes and plants one sentence into each copy. The planted copies are for the
labeller's validation. Each copy carries the tag `planted`, so no other eval reads it as real
work.

| Flag | Meaning |
| --- | --- |
| `--in` | A manifest that names the writes to copy: a set manifest of `split`, or the manifest of `rule-cost`. |
| `--corpus` | The cases file that holds the text of those writes. |
| `--seed` | The seed of every draw. |
| `--per-shape` | The faults of each named shape: `names`, `shows`, `pays` (default 40). |
| `--nolist` | The faults whose verb is not *name*, *show* or *pay* (default 40). |
| `--controls` | Plant one matched control for each fault. |
| `--out` | The directory for the output files. |

The sentences come from templates in `plant.go`. They are written for this command, with generic
subjects and objects, in the present, the past and the future:

- A fault states no fact for its verb and has no literal reading, for example "The new section
  names the risk." Its object is an abstract noun that states no value (the risk, the gap, the
  problem, the cause, the work and the like). Its subject is a text, a change or an approach,
  never a thing that literally displays, carries, holds or pays (a plan, a log, a page, an
  output, a table, a report, a queue). The pays faults have subjects that pay no money (a
  branch, a cache, a scan). The other verbs are *frame*, *capture*, *surface*, *anchor*, *carry*,
  *unblock*, *bridge*, *drive*, *own*, *close*, *track*, *signal*, *reflect*, *move*, *hold* and
  *shape*, used figuratively, taken in turn in a seeded order. *land* and *back* were dropped:
  each has a literal reading a labeller takes for a fact.
  `TestFaultsHaveNoLiteralReadingAndStateNoValue` checks the subjects and objects.
- A control uses the same verb in a sentence that states the fact (a value, a holder, a date, an
  amount or an observed event), or in its literal sense with a fact, as the definition's boundary
  cases allow. Every control states a fact of software work (a file, a command, a test, a build,
  a config value, a count, a person or a date), so a reader of a software write cannot take its
  subject for a thing of another domain. For example: "The config file names `us-west1` as the
  region."

No sentence is planted twice in one run.

The writes come from the writes the manifest names whose main field is not a one-line field
(`issue_title`, `ask_option`, `comment_suggestion`). The families are taken in turn, and each
family's writes in a seeded order, so every family with a body is drawn from. A fault goes in at a
paragraph start of the main field drawn by the seed. Its control goes into a second copy of the
same write at the same paragraph start. A paragraph start is used only when the prose reader reads
the planted sentence there as one sentence of the main field, which the field holds nowhere else.

A planted sentence must not read as someone's relayed or quoted words, so these starts are not
drawn:

- the start right after a block quote, a paragraph that holds a quotation mark (`"`, `“`, `”`,
  `„`, `«`, `»`, `‘`), or a paragraph that ends in a colon;
- the start right before a block quote or a paragraph that holds a quotation mark;
- every start after a paragraph that attributes words to a person (`From X:`; *wrote*, *said*,
  *asked*, *replied*, *answered*; *writes*, *says*, *asks*, *replies* followed by a colon or a
  quotation mark), up to the next heading.

When no other start serves, the first start of the main field is used, and the manifest line holds
`next_to_relayed: true`. When no start of a write serves at all, the next write is taken.

Output, in `--out`:

- `planted.jsonl`: the copies, as cases (every field of each copied write), with `copy` (the copy
  id) and `tag` (`planted`). The main field's `text` and `sha256` are the planted copy's.
- `manifest.jsonl`: one line for each copy: `copy`, `pair` (a fault and its control share it),
  `tag`, `kind` (`fault` or `control`), `shape` (`names`, `shows`, `pays` or `other`), `verb`,
  `sentence`, `sentence_prose` (the sentence as the prose reader reads it, with `CODE` for inline
  code), `event_id`, `source_id`, `family`, `project`, `field`, `surface`, `position` (the byte
  offset of the sentence in the copied field), `boundary` and `boundaries` (which paragraph start
  was used, of how many), and `seed`.
- `sha256`: the SHA-256 of both files.

## label

`label` labels each sentence of each write with a model labeller.

| Flag | Meaning |
| --- | --- |
| `--in` | A cases file, or the `planted.jsonl` of `plant`. |
| `--titles` | A cases file to read issue titles from (default `--in`). For planted copies, give the corpus. |
| `--definition` | The definition file. The command sends it to the model as the rubric. Keep it out of the repository. |
| `--out` | The labels file. The command refuses a file that exists, unless `--resume` is given. |
| `--resume` | Keep the writes the labels file holds with no error, and label the others. |
| `--concurrency` | The calls in flight at once (default 4). |
| `--kind` | The endpoint's protocol (default `anthropic-messages`). |
| `--endpoint` | The model gateway's base URL. |
| `--key-file` | A file that holds the gateway's key. The command never prints it. |
| `--model` | The model id (default `claude-opus-5-5`). |
| `--effort` | The reasoning effort (default `off`, no thinking). |
| `--chunk` | The most sentences one call labels (default 60). |
| `--call-timeout` | The time bound of one model call (default 5m). |

For each write, the command numbers the prose sentences of all its fields (`prose.ProseSentences`)
`s1`, `s2` and so on. It sends one JSON input to `agentloop.Run`: the issue title the write's
issue had at the write's event (the last `issue_title` case of the issue at or before it), the
surface of the main field, each field with its raw text, and the numbered sentences. A second
text block asks for every id: "Label all N numbered sentences, s1 to sN, with one entry each."
The system prompt is an instruction in `label.go` (the text is data; label every numbered
sentence) followed by the definition file. The reply must match the schema
`{sentences: [{id, verdict, shape, rewrite, reason}]}`, with `verdict` one of `vacuous`,
`not_vacuous`, `unresolved` and `shape` one of `names`, `shows`, `pays`, `other`, `none`. A reply
that leaves out a sentence, labels one twice or labels one that was not sent is unusable.

Each call is bounded at `--call-timeout` and 16,000 output tokens. A write whose sentences are more than
`--chunk` is labelled in chunks of `--chunk` sentences, each sent with the two sentences before
and after it as context. A write whose raw text is more than 150,000 characters is sent without
the raw text. A call that fails with a 429, a 5xx or a network error is made again up to 5 times,
after 2 s, 4 s, 8 s, 16 s and 32 s. A call whose reply is unusable, malformed or cut at the output
bound is made again at once, up to 4 times. (The labeller model at times answers for the first
sentence alone; a new call answers for all of them.)

Each line of the labels file is one write: `event_id`, `source_id`, `copy`, `family`, the write
label, each sentence (`id`, `field`, `kind`, `text`, `verdict`, `shape`, `rewrite`, `reason`)
and each call (`chunk`, `sentences`, `raw_text`, `attempts`, `start`, the last attempt's
`latency_ms`, the served `model`, the tokens of all attempts, the `fault`, the `error`, and for an
unusable reply the reply itself in `reply` and `reply_text`). The write label is `failure` when
any sentence is vacuous, `unresolved` when none is and any is unresolved, `pass` otherwise, and
`error` when a call failed (the line then holds `error`, and `--resume` labels the write again).

At the end the command prints the count of write labels and of verdicts, the served model ids,
the call latency and the tokens.

## validate-labeller

`validate-labeller` checks the labeller against the planted cases. It reads only the planted
sentences. A fault must be labelled `vacuous` and a control `not_vacuous`.

| Flag | Meaning |
| --- | --- |
| `--labels` | The labels file of `label` over the planted copies. |
| `--planted` | The `manifest.jsonl` of `plant`. |
| `--seed` | The registered seed. The command fails when a planted case was drawn with another seed. |
| `--out` | The report, Markdown. A `.json` file with the same name holds the same numbers. |

A planted sentence's label is the label of the one sentence of the copy's planted field whose text
is the manifest's `sentence_prose`. A copy with no label, a copy whose label holds an error, and a
sentence not found once are unlabelled.

The report counts the agreeing, disagreeing and unlabelled cases for each class (fault, control)
and case type (family), for each class, for each case type and in total. Each row gives the exact
one-sided upper bound on the disagreement rate at 97.5% (Clopper-Pearson, the quantile of
Beta(x + 1, n − x), with gonum's `distuv.Beta`). The report lists the served model ids and every
disagreeing or unlabelled case.

The labeller passes only with at least 183 agreeing cases in total and no disagreeing and no
unlabelled case in any class and case type. 183 agreeing cases and none disagreeing bound the
disagreement at 2.0% at 97.5%. The command exits 0 on a pass and 1 on a failure.

## Reference labels and bounds

`recall`, `score` and `reproduce` read a labels file of `label` and, for planted copies, the
`manifest.jsonl` of `plant`. Each write is read once; where the file holds a write twice (after
`label --resume`), the last line wins.

- A sentence's reference verdict and shape are the labeller's. In a planted copy, the planted
  sentence (the one sentence of the planted field whose text is the manifest's `sentence_prose`)
  takes them from the manifest instead: a fault is `vacuous`, a control `not_vacuous`, each of the
  planted shape.
- A write's reference label is `failure` when it holds a reference-vacuous sentence, `unresolved`
  when it holds none and an unresolved one, `pass` otherwise, `not_applicable` when it holds no
  prose sentence, and `error` when the labeller failed or the planted sentence is not found once.
  The rates read only writes labelled `failure`, `unresolved` or `pass`.
- A copy with a `copy` id is in the planted stratum; every other write is in the real stratum.
- The no-list sentences are the reference-vacuous sentences of shape `other` (a main verb that is
  not *name*, *show* or *pay*). `--heldout-seed N --heldout-share S` splits them into a tuning half
  and a held-out half: a unit goes to the held-out half when the first 8 bytes of
  `sha256("<seed>\0<unit>")`, as a share of 2^64, fall under S. The unit of a planted fault is its
  verb (`verb:<verb>`), so no verb of the held-out half is tuned on; the unit of a labelled sentence
  is its write (`write:<event id>/<source id>[/<copy>]`). The split depends on the seed and the
  unit alone, so `recall` and `score` given the same seed and share draw the same halves.

Every bound is exact and one-sided (Clopper-Pearson, gonum's `distuv.Beta`): the upper bound on a
rate after x events in n trials at confidence c is the Beta(x + 1, n − x) quantile at c (1 when
x = n or n = 0), and the lower bound is the Beta(x, n − x + 1) quantile at 1 − c (0 when x = 0).
0 of 59 bounds a rate at 4.95% at 95%, and 0 of 183 at 1.99% at 97.5%.

## recall

`recall` measures the code stage alone: for every reference-vacuous sentence of a usable write,
is it a candidate? It runs `candidates.Find` once over each write's sentences (the labels hold
each sentence's field and text in order, which is all the code stage reads), once with the
bundle's lists and once with `Options{NoLists: true}`, and matches each candidate to its sentence
by field and text. Find answers its candidates strongest first, not in document order, so each
candidate takes the first sentence not yet taken that has its field and text.

| Flag | Meaning |
| --- | --- |
| `--labels` | The labels file. |
| `--planted` | Plant's manifest, when the labels hold planted copies. |
| `--heldout-seed`, `--heldout-share` | Split the no-list sentences (default share 0.5). Without a seed, nothing is split. |
| `--out` | The directory for `recall.json`, `recall.md`, and with a split `nolist-tuning.jsonl` and `nolist-heldout.jsonl`. |

The table gives, for each population (each named shape, the no-list sentences, each half, all
reference-vacuous sentences), the sentences, the candidates found, the recall and its exact 95%
lower bound, with the lists and without them. Below it: the candidate share of all sentences and
of all writes, with and without the lists, and whether R13 holds: deleting the lists does not
lower the recall over the held-out half (over all no-list sentences without a split). Only the
tuning half is for tuning; the held-out half is written to its own file so it is never tuned on.

## digest

`digest` prints the bundle's digest and its files. `--root` is the module root (default `.`, so
run it from `packages/envoy`). The files, in this order, as paths from the module root:

1. `internal/dispatch/writingcheck/prose/*.go`
2. `internal/dispatch/writingcheck/rules/*.go`
3. `internal/dispatch/writingcheck/candidates/*.go`
4. `internal/dispatch/writingcheck/reader/*.go`, then `reader/prompt.md`
5. `internal/dispatch/writingcheck/bundle.go`, `checker.go`, `actor.go`
6. `internal/dispatch/writingcheck/baseline/*.go`

Each `*.go` is the directory's Go files without `_test.go`, sorted by name. The digest is the
SHA-256 of the lines `<sha256 of the file>  <path>\n` in that order, which is what
`sha256sum <files in order> | sha256sum` prints from the module root.

## score

`score` runs the detector on every write of one stratum of a labels file as the server's check
does: the code stage over all the write's prose sentences, then one reader call over all its
candidates (the reader reads at most 24). The reader is `writingcheck/reader` with the bundle's
prompt and schema.

| Flag | Meaning |
| --- | --- |
| `--labels`, `--planted` | The labels file and plant's manifest (`--planted` is required for `--stratum planted`). |
| `--stratum` | `real` or `planted`. |
| `--out` | The directory for `outputs.jsonl`, `figures.json` and `figures.md`. |
| `--labeller-bound` | The labeller's validated disagreement bound at 97.5%, for the composed bound. |
| `--heldout-seed`, `--heldout-share` | The split `recall` used, for the held-out no-list recall. |
| `--bootstrap-seed`, `--bootstrap` | The seed (default 1) and resamples (default 1000) of the cluster bootstrap. |
| `--root` | The module root, for the bundle digest (default `.`). |
| `--final-test`, `--set-digest`, `--seed`, `--exposure-log` | Mark the run as the final test; see below. |
| `--kind`, `--endpoint`, `--deployment`, `--api-version`, `--azure-tenant`, `--azure-client-id`, `--federated-token-file`, `--scope`, `--key-file`, `--no-effort`, `--deadline` | The reader flags of `probe-model`. The server sends no effort (`writingcheck.ReaderNoEffort`), so give `--no-effort` to match it on `openai-chat`. |
| `--model-id` | The model id the reader pins (`reader.Config.BundleModelID`, default the bundle's). A reply from another model is `model_mismatch`. |
| `--concurrency` | The writes read at once (default 4). Each worker has its own reader, so each call's served model id is its own. |

Each line of `outputs.jsonl` is one write: `write` (`<event id>/<source id>[/<copy>]`), `stratum`,
`sentences`, `candidates` (`id`, the labels' `sentence_id`, `field`, `text`, `reason`), the reader's
`outcome` (`read`, `candidate_cap`, `reader_timeout`, `reader_error`, `reader_malformed`,
`reader_budget`, `model_mismatch`, or `not_called` with no candidate), its `verdicts` (`id`,
`vacuous`, `rewrite`; one for each candidate sent: the model's reply, `{vacuous: [{id, rewrite}]}`,
names only the vacuous ones, and each candidate it does not name is `vacuous: false`), the
scanner `verdict`, `model_called`, `latency_ms`, the served `model`, `tokens_estimated` and
`error`.

The scanner's verdict on a write:

- `not_applicable`: the write holds no prose sentence;
- `failure`: the reader called a candidate vacuous;
- `no_failure`: no candidate, or the reader read every candidate and called none vacuous;
- `insufficient_evidence`: candidates past the cap went unread and none read was vacuous;
- `error`: every reader fault (a timeout, a provider error, a malformed reply, a spent budget, a
  model mismatch). An `error` counts as a missed failure.

The figures, for the scanner and, on the same writes, for the baseline (`baseline.Matches` on
each sentence; a write is refused when any sentence matches):

- **Write precision**: over the refused writes with a usable label, those labelled `failure` (they
  hold at least one reference-vacuous sentence; in a planted fault copy, the planted fault is
  one). `1 − precision` is given with its exact upper bounds at 95% and at 97.5%. A refused write
  labelled `unresolved` counts against precision.
- **Composed bound** (with `--labeller-bound`): the labeller's bound plus the scanner's 97.5%
  upper bound on `1 − write precision`, and whether it is at or below 5%.
- **Sentence precision**: over the sentences the reader called vacuous, those that are
  reference-vacuous, with `1 − precision`'s bounds, and a 95% percentile interval by a cluster
  bootstrap over the writes holding a refused sentence (PCG source seeded with the bootstrap
  seed). The bootstrap describes these cases alone and certifies nothing.
- **Recall**: over the reference-vacuous sentences of usable writes, those the reader called
  vacuous: for each named shape, for the no-list sentences, for the held-out half, and for all.
  **Write recall**: over the writes labelled `failure`, those refused. Each with its count and
  exact 95% lower bound.
- The shares of writes the scanner calls `not_applicable`, `insufficient_evidence` and `error`,
  and the refused writes with no usable label (left out of precision).
- Over the writes a model call was made for: latency p50 and p95 (nearest rank), tokens p95, and
  the ceilings, twice each p95. The reader's `Observe` gives no token counts, so a call's tokens
  are estimated at four bytes a token, as the reader's budget counts: the system prompt, the
  candidates as sent, and the verdicts as returned.
- The served model ids.

`figures.json` holds the run (`run_id`, `time`, `final_test`, `set_digest`, `seed`,
`bundle_digest`, `model_id`, `deployment`, `deadline`, and the SHA-256 of the labels, the planted
manifest and `outputs.jsonl`), the parameters (stratum, split, labeller bound, bootstrap seed and
resamples, and `decimals`, 4), and the figures. `figures.md` is the same as tables.

With `--final-test` (which needs `--set-digest`, `--seed`, `--exposure-log` and a `--model-id`),
the command refuses a set whose digest the exposure log already holds a final-test run for. It
appends one line to the log (`run_id`, `kind: final_test`, `time`, `set_digest`,
`bundle_digest`, `seed`, `model_id`, `stratum`) before it reads any write, so a run that stops
part way still spends the set.

## reproduce

`reproduce` computes every figure again from `outputs.jsonl`, the labels and the planted manifest,
with the parameters `figures.json` records, and calls no model. `--bootstrap-seed` replaces the
recorded seed. It fails (exit 1) and lists each difference when `outputs.jsonl` is not the file
the run stored (its SHA-256), a write's stored verdict does not follow from its outcome and
verdicts, a write of the stratum has no output, or a figure differs from `figures.json` by more
than half a unit of its fourth decimal.

## regress

`regress` is the model half of the regression suite: it runs the detector on development cases
written for the test and compares each named sentence's verdict with the expected one.

| Flag | Meaning |
| --- | --- |
| `--cases` | A JSONL file of `{"id", "body", "expect": [{"sentence", "vacuous"}]}`. The body is read as the field `body`. Each expected sentence must be a prose sentence of its body. The reader's development cases are `internal/dispatch/writingcheck/eval/regress-cases.jsonl`, and a test of that package checks that each of their expected sentences is a candidate of the code stage. |
| `--repeat-bound` | The repeatability bound: the largest share of each class that may disagree. |
| `--out` | A JSON file for the report. |
| reader flags | As `score`. |

A sentence is called vacuous when it is a candidate and the reader called it vacuous. A reader
fault disagrees on every expected sentence of its case. The command prints, for each class
(`vacuous`, `not_vacuous`), the expected sentences, the disagreements and their share, lists each
disagreement, and exits 1 when either share is above the bound.

## reading

`reading` takes R11's reading over a window of agent writes with the frozen bundle.

| Flag | Meaning |
| --- | --- |
| `--in` | The cases file of `fetch`. |
| `--after`, `--through` | The window: events after `--after`, through `--through`. |
| `--exclude` | A manifest whose writes are left out (repeatable): the set manifests of `split`, the manifest of `rule-cost`, plant's manifest. Each line's `event_id` and `source_id` name a write. |
| `--out` | The directory for the output files. |
| `--root` | The module root, for the bundle digest. |
| reader flags | As `score`. |

Every write of the window that no excluded manifest names is read; a planted copy never is. Each
is checked twice by `writingcheck.Checker`, as the server checks it: once with the structure rules
the bundle enables (`writingcheck.EnabledRules`), once with the detector alone.

The report gives the share of writes the detector refuses, the share the rules refuse, the share
either refuses, the share where the detector's outcome is a fault (anything but `refused` or
`clean`), the candidate share of writes and of sentences, and the share of the window's body
bytes inside block quotes and inside fenced code. A line walker counts those bytes: a fenced
block runs from its opening line (three backticks or tildes, after at most three spaces) through
its closing line or the end; a block quote is a line opening with `>` after at most three spaces,
and each non-blank line straight after one.

A window with no agent write outside the excluded manifests is reported as `no reading`, and its
report holds no share, never a zero.

Output, in `--out`: `manifest.jsonl` (`event_id`, `source_id`, `project`, `family` of each write
read) and `manifest.sha256`; `results.jsonl` (each write's sentences, candidates, detector
outcome, whether the rules refused it, and its bytes; no text); `report.json` and `report.md`,
with the window, the excluded manifests and their SHA-256, the projects, the manifest's SHA-256
and the bundle digest.

## validate

`validate` checks a description file (`internal/dispatch/writingcheck/eval/scanner.yaml`, valid
against `scanner.schema.json` beside it) against the method's rules for a gate.

| Flag | Meaning |
| --- | --- |
| `--description` | The description file. A field the schema does not name fails it. |
| `--exposure-log` | The exposure log `score --final-test` appends to. A log that does not exist holds no run. |
| `--manifests` | The directory the manifests' `file`s are in (default: the description's directory). |

A field whose figures wait for the freeze or the final test carries `pending: true` and a
`reason`; the command lists each such field and does not fail on it. It fails (exit 1) on:

- a required field that is absent, or not pending and without its figures, a pending field with
  no reason, fewer than five boundary cases, and no baseline;
- for triage or a gate, no error-analysis record, or a sample under 50 opportunities or under 20
  sessions or issues;
- a manifest whose file's SHA-256 is not its `sha256`, a case with no group, a group in two sets,
  and a case whose evidence comes after its cutoff (`evidence_event_id` above `cutoff_event_id`,
  each the case's `event_id` where absent);
- before the claim (`final_test` pending), any final-test run of the final-test set in the
  exposure log; after it, no such run, more than one, one naming another bundle digest than
  `bundle_digest`, or another run id than `final_test.run_id`;
- an acceptance bar committed after the final-test run: `acceptance_bar.bar_committed_at`, or
  else the committer time of the `acceptance_bar:` line by `git blame` (a line not committed
  fails), against the run's time in the exposure log;
- a final test with no baseline figures;
- for a gate, for each error (1 − precision with e = 1 − `precision_floor`, 1 − recall with
  e = 1 − `recall_floor`): fewer draws than ln(α) / ln(1 − e), an exact upper bound above e at the
  bar's confidence, and, with a validated labeller, the composed bound (the labeller's bound plus
  the scanner's, at 1 − α/2) above e.

`TestValidateRefusesAGroupInTwoSetsAndNoBaseline` is the negative control: a copy of a valid
description with one group in two sets and the baseline taken out is refused for both.

## dataset

`dataset` builds the dataset of INFRA-3231's R1. It holds texts the owner could not read and texts
he read with no trouble. Each case holds Dispatch's record as it stood when the agent wrote the
text. The work has these steps:

1. `fetch` copies the event log up to an end time into one snapshot file.
2. `classify` gives each text that a person wrote one verdict of the selection prompt.
3. `pack` puts the snapshot, the verdicts and the seed, and the result of the confirmation read
   when there is one, into one bundle.
4. `build` reads only those inputs. So the same inputs build the same bytes, and a later edit in
   Dispatch (a changed comment, a new issue title) changes nothing.
5. `export-sessions.sh` writes the writer sessions out of the transcript store, on a machine that
   can read it, from the session list `build` writes.
6. `sessions` checks that export and gives each case its place in its writer session.
7. `owner-texts` writes each text that the owner wrote himself, with its time, from the snapshot.
   `harness score --vocabulary` reads it.

The dataset is a file artifact of INFRA-3231. It is never a file of this repository.
`dataset/DATASET` holds the end time, the seed and the SHA-256 of each input and each output.
`build --from <bundle>` prints the same SHA-256.

`named` is not one of these steps. It builds cases from document versions that it reads from the
live Dispatch, for a text the dataset does not hold (see "named").

### fetch

| Flag | Meaning |
| --- | --- |
| `--end` | The end time, RFC 3339. The snapshot holds the events created at or before it. |
| `--out` | The snapshot file, gzipped JSONL. |
| `--labelled` | The labelled sets (`writing-check-sets-and-labels-tar-gz`), as the tarball or the directory it was extracted to. |
| `--work` | The work directory (default `<out>.work`). |
| `--owner` | The person whose texts select the cases (default `sami@theorem.dev`). |
| `--concurrency` | The reads in flight at once (default 4). |

`fetch` does these steps:

1. It pages `GET /api/v1/events/log` from the first event, 1,000 events a page, across every
   project. It keeps each event created at or before `--end`. It stops when the log ends, or when
   the events are 10 minutes past the end time, because an event that commits late can carry an
   earlier time.
2. It reads the whole body of each comment and message whose event cut it (`body_truncated`)
   from `GET /api/v1/comments/{id}` and `GET /api/v1/messages/{id}`.
3. It reads the document versions that the cases read (`GET /api/v1/artifacts/{id}/versions/{n}`).
   For each agent text that can become a case, it reads each document of the text's issue at the
   version in force just before the text. For a document version, it also reads the version and
   the version before it. These agent texts can become a case: the agent text that each owner
   text answers, the first agent reply to each owner text, each ask he answered by options alone,
   each version he reviewed with no reason, each labelled write, and each text he links on
   INFRA-3231. The verdicts and the seed do not change this set, so one snapshot serves every
   build. The log refers to many more versions, and their text is much larger than all the
   cases' text, so `fetch` reads only these.
4. From `--labelled`, it reads every `labels.jsonl`. It keeps each write labelled `failure`, that
   is, with a sentence the labeller found states no fact. It skips planted copies (a record with
   a `copy` id).

The snapshot is gzipped JSONL. Each line's `kind` is its first field:

| Kind | Holds |
| --- | --- |
| `header` | The first line: `format`, `end`, `owner`, `naming_issue` (INFRA-3231), the first and last event id and the count of each kind of line. |
| `event` | One row of the event log, as Dispatch served it, in id order. |
| `body` | `of` (`comment` or `message`), `id`, and the whole body in `text`. |
| `version` | `artifact_id`, `number`, and the Markdown in `text`. |
| `label` | One labelled write: `event_id`, `source_id`, `family`, the `sets` that label it and the `shapes` of its sentences that state no fact. A label holds no text. |

A body or a version that Dispatch would not serve (404 or 403) has no `text`. Its `unreadable`
field gives the reason.

A stopped `fetch` continues when you run it again with the same flags. The work directory keeps
each page of the log as a file, and the bodies and versions as JSONL. `fetch` refuses a work
directory made for another end time or owner. `fetch` makes at most 20 requests a second across
all its reads, and retries a request as `fetch` does above. It prints the snapshot's SHA-256.

### classify

`classify` reads every text that a person wrote for themselves: an event whose actor is a `user`
with no acting session. These texts are comments, messages, asks, answers to asks, notes on asks
(a decline or a resolution) and the reasons of reviews. It does not read a person's issue titles
or document versions, or the reason Dispatch itself writes when it carries an approval forward.

Each call sends the selection prompt, `internal/dispatch/wordrecords/select.md`, as the system
prompt. The user message holds the agent text that the person's text replies to (see "The reply
walk" below), cut at 12,000 bytes; the part of a document the text points to, when it points to
one; and the person's text, cut at 8,000 bytes. The reply is one object of a schema: `verdict` is
one of `asks_word_meaning`, `cannot_understand`, `jargon_complaint`, `asks_agent_question` and
`none`, and `words` lists the words the person asks about. The server's records job (R8) sends the
same prompt and builds its user message with the same code (`wordrecords.Input`), so one
definition of a question about words serves both; `select_prompt_sha256` in `dataset/DATASET` is
the prompt's SHA-256.

| Flag | Meaning |
| --- | --- |
| `--snapshot`, `--out` | The snapshot, and the verdicts file (JSONL). |
| `--model` | The Claude model (default `claude-sonnet-4-6`). The call is sent at temperature 0 with thinking off. |
| `--endpoint`, `--key-file` | The model gateway, Anthropic messages (default `$ANTHROPIC_BASE_URL` and `/var/run/model-gateway-key/key`). |
| `--concurrency` | The calls in flight at once (default 8). |
| `--limit` | Classify at most this many texts in this run (default 0: all). |

Each line of the verdicts file holds the text's id (`text`: its key and its event id), the event,
the person, the `verdict` and its `words`, the `model` the provider served, the model asked for,
the prompt's SHA-256 (`prompt_sha256`), the user message's SHA-256, the tokens and the number of
attempts. A call that fails with a 429, a 5xx, a network error or the time bound is tried again up
to 6 times, after 2 s, then twice as long each time. A reply that is not one verdict of the schema
is asked again at once, up to 3 times. A text that still has no verdict gets a line with an
`error`. The next run classifies each text that has no line without an error. The summary prints
the verdicts of the owner and of the other people, the served models, the prompt's SHA-256 and
the tokens of every call that the file records.

### The reply walk

A text's parent is the text it replies to:

- For a comment, its parent is the comment it answers (`answers`, else `reply_to`), its ask
  (`ask_id`), or the document version its anchor points to.
- For a message, its parent is the message in `in_reply_to`.
- For an answer or a note on an ask, its parent is the ask. For an approval request that holds
  no words of its requester, its parent is the document version the request asks about.
- For a review, its parent is the version it reviews.

The agent text that a person's text answers is the first agent text up its chain of parents. When
the chain holds no agent text, it is the last agent text before it in its thread. For a message
on no issue, it is next the last agent text in its conversation with that session. Last, it is the
last agent text on its issue. A case's `found` field says which rule found it (`reply_chain`,
`thread`, `conversation`, `issue`). The first agent reply to a text is an agent text whose parent
is that text (`direct_reply`). When there is none, it is the first agent text after the text in
its thread or its conversation.

The text of a document version is the blocks that the version before it does not hold, as `fetch`
reads them. An agent's text is a text that `writingcheck.Checks` checks.

### build

| Flag | Meaning |
| --- | --- |
| `--snapshot`, `--verdicts`, `--seed` | The three inputs. |
| `--confirmed`, `--confirmed-seed` | Optional, together: the result of a fresh reader's read of a sample of question cases (`{"reader", "confirmed", "total", "items": [{"case_id", "about_words"}]}`), and the seed of its draw. |
| `--from` | A bundle `pack` wrote, in place of the other inputs: the tarball, its manifest, or a directory that holds one of them. |
| `--out` | The directory for the output files. |

`build` refuses a verdicts file that has no verdict for one of the owner's texts. It makes the
four groups of R1:

- **question**: each owner text with the verdict `asks_word_meaning`, `cannot_understand` or
  `jargon_complaint`, with the agent text it answers.
- **answer**: the first agent reply to each owner text with the verdict `asks_agent_question`.
  Each text the owner links in his texts on INFRA-3231 (`.../comment/<id>`, `.../ask/<id>`,
  `.../message/<id>`) is also in the dataset, in the group it fits. A link to his own question
  about words is that question case. A link to his other text gives the answer case of its first
  agent reply. A link to an agent text marks that text's cases, or else makes it an answer case.
  Such a case has `named_by_owner`.
- **phrase**: an agent text that the owner corrected for one of the six phrase shapes of
  `skills/plain-words`. The owner's text has a verdict about words and names the shape inside
  quotes, and the agent text holds the shape, by the expressions of INFRA-3231's decisions
  document. Each labelled write of an R1 kind is also a phrase case.
- **plain**: agent texts that the owner answered by the reply chain or in their thread, or acted
  on (an ask he answered by options alone, a version he reviewed with no reason). No owner text
  that answers them has a verdict other than `none`, and they are in no other group. The seed's
  hash orders them, and `build` takes as many as there are question cases.

A broadcast is one write of the owner. Its first copy is his text, with its verdict; each later
copy holds the same body and is no text of his, so `classify`, the counts and the groups read the
broadcast once. Each copy still opens its own recipient's thread, so a record in that thread holds
it.

Each case holds these fields:

| Field | Meaning |
| --- | --- |
| `id`, `group` | `<group>/<text key>@<event id>`. For a question case, the text is the owner's question. For the other groups, it is the agent text. |
| `agent_text` | The agent's text: its key, event, time, kind, family, source id, actor, session, text and SHA-256. |
| `owner_reply` | The owner's text that answers the agent text, with its verdict (verdict, words, model, prompt SHA-256). For a plain case with no such text, the act (`kind: act`). |
| `question` | For an answer case, the owner's question that the agent text answers. |
| `phrase` | For a phrase case, the shapes and the sources (`correction`, `labelled:<set>`). |
| `kind` | `ask`, `comment`, `message` or `document`. |
| `conversation` | `true` for a reply in a thread, to a message or on an ask. `false` for a text outside a conversation. |
| `writer_session`, `write_time` | The session that wrote the agent text, from the event's actor (the session, or the session that acted for a person), and the time it wrote it, which is where its session is cut. |
| `record` | Dispatch's record just before the write: the project; the issue with its title then, its parent and its root; each of the issue's documents at the version then in force, under the name it had then (`artifact_id`, `name`, `version`, `sha256`; an issue's primary document is `spec.md`, the name Dispatch gives it); and the texts of the agent text's thread written before it. |
| `tree`, `split_group`, `part` | The case's issue tree, its split group, and `held_out` or `development`. |
| `found`, `named_by_owner` | How the agent text was found, and whether the owner links the case on INFRA-3231. |

The split follows Decision 17 of the plan. A case's tree is the root of its issue. For a message on
no issue, the tree is its session's conversation. For a project document, the tree is the
document. `assignGroups` (`group`) joins trees that share a document or a near copy (word 5-gram
Jaccard at least 0.9) into split groups. A split group's key is its least tree. Each split group
goes whole to one part. The split groups are taken in the order of a SHA-256 of the seed and
their key. A split group goes to the held-out part when that brings each case group's held-out
share, taken together, nearer 40%: the sum over the four case groups of the squared distance of
each held-out share from 40% falls. A split group that holds cases of several case groups counts
in each. `build` fails if one tree is in both parts.

`build` writes these files to `--out`:

- `cases.jsonl.gz`: the cases, by id.
- `documents.jsonl.gz`: the Markdown of each document version that a record names (`artifact_id`,
  `version`, `sha256`, `markdown`).
- `sessions.tsv`: the session list that `export-sessions.sh` reads: one line
  `<session id>\t<cut time>` for each writer session, its cut the latest `write_time` of its
  cases, in UTC. A writer session whose id is not a UUID, which the transcript store cannot hold,
  is left out and counted.
- `counts.json`: the counts for each group, by case kind, writer session (present or absent, and
  written before the transcript store's last load), conversation flag, `found`, and held-out and
  development sizes. It also holds the parts' cases, split groups and trees,
  `no_tree_in_both_parts`, the owner's verdicts, the served models, the plain pool, the owner's
  links, the labelled writes, the cases left out with the reason, the sessions listed and left
  out of `sessions.tsv`, and the SHA-256 of the other files. It holds no text.

With `--confirmed`, `counts.json` also holds `confirmed_sample`: the cases the reader confirms
ask about words, the cases it read, the seed, the draw (`python3
random.Random(<seed>).sample(sorted question cases by id, <total>)`, which drew the sample), the
reader, and the result file's SHA-256. `build` refuses a result whose counts do not match its
items, a case read twice, and a case that is not a question case of this build. The cases do not
change.

`build` prints the SHA-256 of `documents.jsonl.gz`, `sessions.tsv` and `cases.jsonl.gz`.

The verdicts file holds the verdict of each person's text. A harness that gives a reviewer the
words a reader asked about before (Decision 19) takes only the verdicts of texts written before
the case's `write_time`. The case's own owner reply comes after that time.

### pack

`pack` writes a gzipped tarball of `seeds.json` (`{"seed": N}`, with `"confirmed_seed": N` when
the bundle holds a confirmation read), `snapshot.jsonl.gz`, `verdicts.jsonl` and, from
`--confirmed`, `confirmed.json`. The tarball holds no time, owner or mode of the files, so the
same inputs write the same bytes. A bundle larger than `--part-size` (default 24 MiB, below
Dispatch's 25 MiB file limit) is also written as parts, `<bundle>.part-000` and on, with
`<bundle>.manifest.json`. The manifest gives each part's size and SHA-256, and the bundle's.
`build --from` the manifest joins the parts. It refuses a part or a joined bundle whose SHA-256
differs from the manifest.

`TestDatasetFlippedVerdictChangesTheDigest` is the negative control of
`TestDatasetBuildIsDeterministic`: the same inputs build the same SHA-256, and one changed verdict
changes it.

### export-sessions.sh

`export-sessions.sh` runs on a machine whose `tq` reads the transcript store. A Legion pod cannot
reach the store today.

```sh
export-sessions.sh --sessions sessions.tsv --exclude exclude.txt --out <empty dir>
```

`--sessions` is the `sessions.tsv` that `build` writes. `--exclude` holds one session id per line:
the sessions to leave out, whose rows are never read. For each batch of sessions (50, or `BATCH`),
the script runs `tq sql --format JSONEachRow` over the store's `messages` table. It reads the
DISTINCT rows of each session at or before its cut time, with no row of kind `file`, in the
order session, `seq`, `raw_file`, `raw_line`. The columns are `session_id`, `host`, `seq`, `ts`,
`role`, `kind`, `text`, `raw_file` and `raw_line`. `TQ` names the `tq` command.

Before it writes a row, the script masks secrets: in each text value of the row, every match of a
secret pattern of the transcript store's `redact.py` becomes a marker that names the pattern
(`[REDACTED:<name>]`). It writes the rows again as JSON with each character as itself.

It writes gzipped JSONL parts, `part-000.jsonl.gz` and on, each under 24 MiB, and `manifest.json`:
`schema` (`infra-3231-session-export/2`), `columns`, `parts` (`name`, `bytes`, `sha256`),
`rows_by_session` (0 for a session the store holds no row of), `masks_by_session` (for each
session with a mask, the masks by pattern), `sessions_missing` and `sessions_left_out` (a count).
It prints the rows found for each session. Schema `infra-3231-session-export/1` is the same
without the masks and their counts.

The sessions this dataset reads, which `dataset/DATASET` records as `sessions_read`: every writer
session of the dataset, and none is left out, by the owner's answer
(dispatch://INFRA-3231/ask/5cfc36a0-1965-46b1-a60e-104869be6c62), so `--exclude` is an empty
file. Session text reaches Claude models only (R2). Every text value is masked for secrets before
the export.

### sessions

| Flag | Meaning |
| --- | --- |
| `--manifest` | The `manifest.json` that `export-sessions.sh` wrote. Its parts are read beside it. |
| `--cases` | The build directory, or its `cases.jsonl.gz`. |
| `--out` | The directory for the output files. |

`sessions` reads an export of schema `/1` or `/2`. It carries the manifest's schema and
`masks_by_session` into `sessions-counts.json`.

`sessions` checks each part's SHA-256 against the manifest, and refuses the export on the first
part that differs, naming it. It also refuses an export made from another session list: a
session the export holds that no case names, or a count of the cases' sessions absent from the
export other than the manifest's `sessions_left_out`. It refuses parts whose rows of a session
differ from the manifest's count.

A case's place in its writer session is the last row, in the export's order, whose `ts` is at
or before the case's `write_time`. Each case gets one status:

| Status | Meaning |
| --- | --- |
| `found` | The session has rows at or before the write. |
| `no_row_before_write` | The session's first row comes after the write. |
| `session_missing` | The store holds no row of the session (`rows_by_session` is 0). |
| `session_left_out` | The export left the session out (`--exclude`). |
| `session_not_a_store_id` | The session id is not a UUID, so `sessions.tsv` does not list it. |
| `no_writer_session` | The case has no writer session. |

`sessions` writes these files to `--out`. No session text is stored once per case:

- `session-rows.jsonl.gz`: each row of the export once, as `session_id`, `index` (its place in
  its session, from 0) and `row` (the row as the export holds it).
- `places.jsonl`: for each case, `case_id`, `session_id`, `status`, `rows` (the case's session is
  the rows of its session with an index below it), and the `ts` and `seq` of the last of them.
- `sessions-counts.json`: the manifest's SHA-256, the parts, the rows, the sessions in the
  export, with rows, missing and left out, the cases by status, and the SHA-256 of the other two
  files. It holds no text.

It prints the counts: the cases with a session (`found`), the sessions missing and left out, and
the cases whose session is missing or left out.

`TestExportSessionsScriptFeedsDatasetSessions` runs the script against a stand-in `tq` and then
`sessions` over its output. Its negative control changes one byte of a part, and `sessions`
refuses it.

### owner-texts

| Flag | Meaning |
| --- | --- |
| `--snapshot` | The snapshot that `fetch` wrote, which `build` reads. |
| `--from` | A bundle that `pack` wrote, in place of `--snapshot`: the tarball, its manifest, or a directory that holds one of them. `owner-texts` reads the bundle's snapshot. |
| `--out` | The directory for `owner-texts.jsonl.gz`. |

`owner-texts` writes each text that the owner wrote himself to `owner-texts.jsonl.gz`, in event
order. The owner is the person the dataset is about. The snapshot's header gives his login.

It reads the snapshot, which is an input of `build`, and not the build's output. The cases hold
only the owner's texts that answer a case, and the snapshot holds all of them. It reads no verdict
and no seed, so one snapshot writes the same bytes.

The file holds the texts that `build` reads as his: his comments, messages, asks, answers to asks,
notes on asks and the reasons of his reviews. It holds no text that a session wrote for him. It
holds no issue title and no document version, because the snapshot holds only the document
versions that the cases read.

Each line holds one text as a case holds a text: `key`, `event_id`, `event_type`, `time` (the
time of the event that wrote it), `kind`, `actor_kind`, `actor`, `text` and `sha256`.
`owner-texts` prints the count of texts and the file's SHA-256.

### named

`named` builds cases from the document versions that `--doc` lists. It reads them from the live
Dispatch, whose address and token file are `DISPATCH_URL` and `DISPATCH_TOKEN_FILE`, as for
`fetch`. Use it for a text the dataset does not hold, such as a version written after the
dataset's end time. It reads no snapshot, no verdict and no seed, and it changes no file of the
dataset.

| Flag | Meaning |
| --- | --- |
| `--doc` | `<issue>/<document>@<version>[,<version>...]`: versions of one document of an issue. `<document>` is the document's slug or its file name. Give the flag once for each document. |
| `--out` | The directory for the output files. |
| `--owner` | The person the texts are written to (default `sami@theorem.dev`). `harness load` gives each case a reader in his place. |

```sh
go run ./cmd/writing-check dataset named --doc INFRA-3577/open-items-md@30,32,33 --out $TMPDIR/named-3577
```

Each version is one case:

- Its `id` is `plain/named:<issue>/<slug>@<version>`, for example
  `plain/named:INFRA-3577/open-items-md@30`. One version always gets the same id.
- Its `group` is `plain` and its `part` is `development`. `named` writes no held-out case.
- Its text is the version's whole Markdown. `build` takes only the blocks that the version
  before it does not hold.
- Its `agent_text` holds the event that wrote the version: the event's id, type, time and
  actor. Its `writer_session` is the session of that event. A version that a person wrote
  himself, or that a Legion daemon wrote, has no writer session, and `named` says so on stderr.
- Its `record`, `tree` and `split_group` are the ones `build` gives a document version. The
  record holds the issue with its title at that time, its parent and its root, and each document
  of the issue at the version in force just before the write. For the version's own document,
  that is the version before it.
- It has no `owner_reply`.

`named` makes these reads:

1. `GET /api/v1/issues/{key}/artifacts/{document}` for each `--doc`: the document's id, slug and
   kind. It refuses a file and an image.
2. `GET /api/v1/issues/{key}/events` for each issue, 200 events a page. These are the rows of
   `GET /api/v1/events/log` that belong to the issue and its documents. `named` gives them, in id
   order, to the index that `build` fills from a snapshot, so the same code makes each record.
3. `GET /api/v1/issues/{key}` for each ancestor of the issue: its parent.
4. `GET /api/v1/artifacts/{id}/versions/{n}` for each version of `--doc` and each version in a
   record. A version of a record that Dispatch does not serve (404 or 403) is `missing` in the
   record, as in `build`.

`named` writes `cases.jsonl.gz`, `documents.jsonl.gz` and `sessions.tsv` in the formats of
`build`. Its `counts.json` is shorter than the one of `build`. It holds the `owner`, which
`harness load` reads; the versions (`named`); the count of cases; the sessions listed in
`sessions.tsv` and left out of it; and the SHA-256 of the other three files. `harness load
--cases` and `harness score --cases` read the directory as they read a build directory.
`harness load` also needs the dataset's verdicts file (`--verdicts`), for the owner's word records
written before each case.

`harness score` scores a case with no session when the directory of `--sessions` holds no place
for it. To give these cases their sessions, run `export-sessions.sh` with this `sessions.tsv`,
then `dataset sessions` with this directory, and give `harness score` the directory that
`dataset sessions` writes.

A case of `named` is always in the development part, and `named` does not read the dataset.
Before you score a version, read the `tree` and `part` of the dataset's cases: if the dataset
holds the version's issue tree in its held-out part, development rounds would read texts of a
held-out tree.

Two runs with the same `--doc` values write the same bytes while Dispatch holds the same events.
A later rename of a document changes its name in the record, and a later move of the issue under
another parent changes the record's parent and root.

`TestDatasetNamedWritesCasesTheHarnessLoads` runs `named` against a stand-in Dispatch and loads
the output with the harness's case reader (`harness.LoadDataset`).

## harness

`harness` (package `cmd/writing-check/harness`) scores the changes of INFRA-3231's R3 on the
dataset of R1, by the score of R2. For each case, the change gives the text a person sees. A
fresh reader answers the person's question from that text alone. The grader then gives each
judgement of R2, one prompt for each, against Dispatch's record as the dataset stored it and,
where the case has one, the writer's session up to the text. It scores change 1, the text as the
person saw it, changes 2 to 7 alone, and their combinations (see "The changes").

Its inputs are in the package, and they are among the paths that decide a score:

- `candidates.yaml`: the roles (grader `claude-opus-5`, writer `claude-opus-5-5`, fresh reader
  `claude-sonnet-4-6`), the candidates, and INFRA-3483's four deployments, pending until they
  have ids. The harness refuses a role that is not Claude, a role that is a candidate, and a
  grader that plays the writer or the fresh reader. A candidate that sets an `effort` is the model
  at that reasoning effort, a candidate of its own named `<id>:<effort>`, such as
  `Kimi-K3.1-Pro:off` beside `Kimi-K3.1-Pro`. Its effort is one of `off`, `minimal`, `low`,
  `medium` and `high`, the efforts the local Dispatch takes in a test model's name, and it has the
  price of its id. A Foundry candidate with no effort sends none.
- `prices.yaml`: each model's price in USD per million tokens, with the source and the date of
  each family's prices. The harness refuses a model with no price. A Claude row's cache write is
  the price of a five-minute write, the life the harness's cache marks ask for (see "The prompt
  cache").
- `prompts/`: `reader.md`, and one `grade-*.md` for each judgement, the system prompt of its
  calls: `answered`, `first-paragraph`, `lost`, `added`, `phrases`, `undefined`, `answer-start`,
  `harder` and `define`. `parts.md` holds the parts they share.
- `writer/writer.md`: the prompts of the writer the harness plays. They are apart from
  `prompts/`, so change 1's prompt digest does not move with them; a run that plays the writer
  records their digest as `writer_sha256`.
- `combinations.yaml`: the combinations of changes `score --change` takes by name.

Every file the harness writes holds Dispatch text or session text. Write them under `$TMPDIR`.
Each subcommand refuses an `--out` inside a repository: a directory at or above it that holds a
`.git` or a `.jj`.

### Model flags

| Flag | Meaning |
| --- | --- |
| `--gateway`, `--gateway-key-file` | The model gateway's base URL (default `$ANTHROPIC_BASE_URL`) and the file of its key, read on each call and never printed. Every Claude model is called there. |
| `--foundry-endpoint`, `--foundry-api-version` | The Foundry account's models inference endpoint and its `api-version`, for a Foundry model. |
| `--azure-tenant`, `--azure-client-id`, `--federated-token-file`, `--scope` | The keyless Entra sign-in of the Foundry account, as `probe-model` takes it. The token file defaults to the pod's ServiceAccount token. |
| `--concurrency` | The model calls in flight at once, across every model (default 6). |

A call that meets a 429, a 5xx, a network error or its time bound is made again after a wait
that starts at 2 s and doubles, up to 6 times. A reply that is not the object its schema names
is asked again at once, up to 2 times. A call with no reasoning asks for temperature 0, which
only a model that takes sampling settings receives: the Anthropic client drops it for
`claude-opus-5`, `claude-opus-4-7`, `claude-opus-4-8` and the `-5` families (`claude-sonnet-5*`,
`claude-fable-5*`), which sample at the provider's own setting. So the grader `claude-opus-5`
does not sample at temperature 0, and its repeatability is measured, not assumed.

### The prompt cache

The provider caches a prompt's prefix up to a call's cache mark, and a later call that sends the
same prefix reads it at the cache read price. Anthropic builds the prefix from the tools, then the
system prompt with the reply's schema, then the messages. So two calls share a prefix only when
they send the same model, system prompt and schema, and the same first blocks of the user turn.

Each call sends what it sent before the harness marked prefixes for the cache, with one change:
a grading call's session part now comes before its subject. Apart from that move, the user turn
holds the old text, cut into blocks so that a mark can end a prefix.

- A grading call sends its judgement's own prompt (its `grade-*.md`) as its system prompt, and its
  judgement's own reply schema. Its user turn is two blocks. The first holds the case's parts that
  the judgement's input mode gives, in this order: the question, the record, the writer's answer,
  and, after a blank line, the session part. The second holds the subject: the reader's answer,
  the two texts, the text with the owner's own words, or the definition. Before the cache marks,
  the subject came after the question, and the session part ended the message. The subject moved
  to the end so that the first block, which each call of the judgement for the case sends the
  same, holds the session part. The mark goes at the end of the first block, where the call is
  marked. `TestTheAnsweredCallSendsItsPromptOfBeforeTheCacheMarks` holds `answered`'s system
  prompt to the bytes it had before.
- `phrases` sends its prompt as its system prompt, and its candidate sentences in their tag,
  `<candidates>`, as before. The tag's first line is a block of its own, so the call can mark the
  prefix that holds the prompt: agentloop marks the end of a call's first blocks (its history),
  and never the system prompt. The prompt is the same for every text.
- The writer sends its turn in the order it always has: Dispatch's record, its text, Dispatch's
  message, and, after a blank line, its session part. The record is a block of its own, and the
  mark goes at its end, where the call is marked. `score --writer-order session-first` sends the
  session part at the end of the record's block instead, before the text and the message, and
  `run.json` records the order as `writer_order`.
- The fresh reader's call carries no mark: its prompt is small.

The harness's first cache layout (2026-11-16) sent one system prompt for every judgement (the
sentences about Dispatch, and one sentence that said the tagged parts were data), one reply schema
that was any of the judgements' own schemas, and each judgement's prompt in the user turn after
the case's parts. A reply in another judgement's shape was asked again. The writer sent the record
and the session part first. So the judgements of a case shared one prefix, but the grader's
grades moved beyond its noise. On the same 229 fresh reader answers, the first cache layout's
grades agreed with the old layout's on 90.8% of `answered`, 88.2% of `first_paragraph`, 75.0% of
`undefined` (any finding) and 92.3% of `phrases`. Two runs of the old layout agree on 97.3%,
95.8%, 89.7% and 97.9%. Change 2's question score also fell, and the writer's order was its only
changed input. So the harness sends each judgement's own prompt and schema again.
`--writer-order session-first` keeps the first layout's writer order, so a change can be scored
with each order.

A call is marked only where at least two calls of the run are known, before the first is sent, to
send its prefix. A prefix that one call alone sends goes unmarked: it pays the input price, not
the cache write at 1.25 times that price. The blocks a call sends, and their order, are the same
whether it is marked or not.

- The grader counts its calls before it sends them (`Grader.Plan`). `score` counts the calls of a
  case's judgements before the case's first grading call. A lockstep run (see "Lockstep") counts
  the calls of the case's texts of every model before the first of them. `grader-test` and
  `phrases-labels` count the calls of every input before their first call. A judgement counts one
  call, and `phrases` one call for each 30 candidate sentences. A judgement whose grade the run
  takes from `--grades-from` counts none. A judgement of the fresh reader's answer counts one call
  while the reader has not answered yet.
- The counts add up over the run, and over every model of a lockstep run. A grading call is
  marked where its prefix counts two or more calls. Each judgement has a system prompt and a
  schema of its own, so only the calls of one judgement share a prefix. In a run of one model a
  judgement makes one call for a text, so the calls of one prefix are the `phrases` calls (below)
  and the calls of one judgement for cases whose parts are the same, byte for byte; the other
  grading calls are unmarked. In a lockstep run, the models' calls of one judgement for a case
  read the same parts where the grader reads the same writer's answer for each (changes 5 and 6
  share one answer, see "Lockstep"; the other changes have none), so they are marked where the
  run has two models or more.
- Every text's `phrases` calls send the same prefix, so a `phrases` call is marked once the run
  has counted two or more of them. In `score`, a case counts its calls only when it starts. So
  the first texts' `phrases` calls can go unmarked when no other case has counted its calls yet.
- The grader makes the first marked call of each prefix alone, and the others wait for its
  reply, since the provider serves a cached prefix only once a call that wrote it has begun to
  answer. An unmarked call waits for none.
- A fallback grader's call is unmarked: no count knows that it will be made.
- The writer marks the calls of a prompt where the case's calls of it can be two or more. Its
  calls of a case send the same prefix: the prompt, its schema and the record, and under
  `--writer-order session-first` the session part. Change 3 calls the writer once in each round
  of review, up to `--rounds` (default 2). So change 3 marks its writer calls where `--rounds` is
  2 or more. Changes 2, 5, 6 and 7a call the writer once for a case, and change 4 does not call
  it. In a run of one model their calls are unmarked.
- A lockstep run counts the writer's calls of a case over its models, as the grader counts its
  own: the calls of one model times the models that make the change of the case. So change 7a's
  writer calls, and change 3's with `--rounds 1`, are marked where two models or more score the
  case. The models make the change of a case one after another, so each writer call after the
  first reads the prefix from the cache. The count is of the calls the models can make: where the
  check of only one model finds something, its one writer call is marked all the same. The side
  answer of changes 5 and 6, which one call makes for every model (see "Lockstep"), counts as one
  call and stays unmarked.

The mark is not one of a grade's inputs. `input_sha256` (see "Grades of another run") is the same
for a marked call and an unmarked one, so `--grades-from` takes grades across the two.

The mark has a life of five minutes (agentloop's `HistoryMarkOnly`), and only the prefix's end is
marked, so no call pays a cache write on its own part. The gateway gives a mark that names no life
one hour, at twice the input price; the five-minute write costs 1.25 times the input price.
`prices.yaml` prices the cache tokens, and each call records them as `cache_write_tokens` and
`cache_read_tokens` beside `input_tokens`, which counts only the prompt tokens read at the input
price. Each call records `marked_calls` or `unmarked_calls`: the model calls it holds that the
harness sent with the mark and with none. `scores.md`'s model table counts them in its Marked
and Unmarked columns, and `grader-test.md` gives them in its cost line. A candidate call is in
neither: the local Dispatch, the reviewer component or the detector's reader sets its marks.

The gateway serves each Claude model from more than one deployment and sends each call to one of
them. A deployment reads only the prefixes that it wrote, so a prefix is written once on each
deployment that its calls reach (measured 2026-11-16: `claude-opus-5` has two).

### Pacing

`score --uncached-input-per-minute N` holds the process's own model calls to at most N uncached
input tokens in any one minute. `grader-test` takes the same flag. The default, 0, paces no call;
the calls are still counted. Uncached input is the prompt tokens that the provider bills at the
full input price: a call's input tokens less the tokens it read from the cache. A cache write
counts.

Every model call of the process passes the pacer (`llm.Pacer`, which the run's `llm.Runner`
holds). These are the grader and its fallbacks, the writer and the fresh readers, with every
retry and every reply asked again. They are also the calls to the candidate that the harness
makes itself: change 7a's detector reader, and Rewrite and Define where the harness calls the
component (see "The changes"). A lockstep run (see "Lockstep") is one process: one pacer holds
the calls of all its models together to N.

- Before each attempt, the pacer charges the attempt's estimate: the bytes of its system prompt,
  schema, shared blocks and user message, at four bytes a token. The pacer leaves the prefix out
  of the estimate only when the attempt is marked (see "The prompt cache"), an earlier marked
  attempt sent the same prefix (the model and its effort, the system prompt, the schema and the
  shared blocks), its reply reported the prefix written to the cache or read from it, and that
  attempt began less than five minutes before. Else the whole prompt is charged.
- An attempt waits while the charges of the last minute leave no room for its charge. The
  attempts go through in the order they came.
- After the reply, the charge becomes the reply's input tokens plus its cache write tokens. A
  reply that reports no counts keeps the estimate. A charge that falls gives tokens back, and an
  attempt that waits for them goes at once.
- An attempt larger than N goes when the last minute holds no charge. No other attempt goes in
  the minute after it.
- A candidate call that the harness makes itself builds its own prompt, so the pacer has no
  estimate of it. It is charged nothing before it starts, it waits while the last minute leaves no
  room, and the tokens its model calls report count when it ends.

The flag does not pace the candidate calls that the local Dispatch makes: the reviewer's draft
route (changes 3 and 4, and 7b), the Rewrite and Define routes (changes 5 and 6). The local
Dispatch is a process of its own, and only its own settings bound those calls.

`run.json` records the setting as `uncached_input_per_minute` (0 for none), so a run that
continues in a directory must give the same value. `scores.md` gives the uncached input of this
run's own calls and the most of it in any one minute; `scores.json` holds them as
`uncached_input_this_run`. In a lockstep run these are the whole process's, in each model's
`scores.json`. The busiest minute can pass N: an attempt larger than N goes alone, and a reply
can report more than its estimate. `grader-test` records the setting and the same counts in
`grader-test.json` and `grader-test.md`.

### Session text

Session text reaches Claude models only. Package `claude` reads the session export, and its
`SessionText` type holds a case's part of a session. No code outside `claude` can read the text:
the type has no exported field and no method that answers it, and it prints as a placeholder.
Only `claude.Client.CallWithSession` sends it, and a `claude.Client` always calls a Claude
model. Package `llm`, which holds the Foundry client, cannot name the type, since `claude`
imports `llm`. So a call that sends session text to a model of another family does not compile.
`TestOnlyTheClaudeClientTakesSessionText` and `TestSessionTextCannotBeReadOutsideItsPackage` hold
that guarantee.

A case's session part is its session's rows before the case's place, the newest
`--session-chars` bytes (default 30,000). It keeps what the people and agents said, what the
writer did and saw, and compaction summaries. It leaves out the writer's thinking, notices that a
plugin adds, and a row the export holds once for each host. Each row is cut at 2,000 bytes. The
parts are cached in `session-excerpts.jsonl.gz` in the output directory.

### probe

`probe` calls each role model and each candidate `--calls` times (default 20) with one short
message, and prints a table: the answers, the statuses, the `route_not_found` answers (Azure's
`ms-azureml-model-error-reason` header), the model ids the provider served, and the latency. A
Foundry candidate at an effort is asked for it (`reasoning_effort`, `off` sent as `none`), and its
row bears its name, `<id>:<effort>`; a Claude call asks for no thinking. A model fails when a call
answers `route_not_found`, a 404 or a 400 that names the model, when the provider serves another
model's id, or when no call answers. Then `probe` exits 1. `--out` also writes the rows as JSON.

### score

| Flag | Meaning |
| --- | --- |
| `--change` | The change to score: `1` to `6`, `7a`, or a combination of `combinations.yaml` (see "The changes"). |
| `--model` | The candidate the change calls: the reviewer's, Rewrite's and Define's model and the detector's reader, for a change that holds 3, 4, 5, 6 or 7a. It is named as `candidates.yaml` names it: its id, or `<id>:<effort>` for a candidate that sets its effort. The `Dispatch-Test-Model` header carries that name. |
| `--models` | In place of `--model`: candidates, by name as `--model` takes one, comma-separated, that one process scores the change with, case by case (see "Lockstep"). Each model's run goes to `<out>/<model>`. |
| `--local`, `--loaded` | The `local.json` of `harness local` and the `loaded.jsonl` of `harness load`, for a change that holds 2 to 6. |
| `--rounds`, `--unedited` | Change 3: the reviews of one text (default 2), after which the writer's last edit posts; and what posts for a case with no session, as `DISPATCH_REVIEWER_UNEDITED` says (`rewrite`, the default, or `as_written`). |
| `--writer-order` | The order of the writer's user turn, for a change that plays the writer: `session-last` (the default), Dispatch's record, the writer's text and Dispatch's message, then the session part; or `session-first`, the record and the session part, then the text and the message (see "The prompt cache"). `run.json` records it as `writer_order`. A change that plays no writer refuses `session-first`. |
| `--cases`, `--sessions` | The build directory of `dataset build`, or the directory of `dataset named`, and the directory of `dataset sessions`. |
| `--out` | The run's directory, or under `--models` the directory that holds each model's run directory. A run continues the cases that have no result in it, and refuses a directory whose `run.json` records other parameters. |
| `--part` | `development` (the default), or `held_out`, which only this flag selects. |
| `--groups` | The case groups to score (default all four; `question` alone for a change that holds 6). |
| `--sample`, `--sample-seed` | A stratified sample of about this many cases (see below), drawn by the seed. |
| `--seed`, `--draws` | The bootstrap's seed (default 3231) and resamples (default 10,000). |
| `--session-chars` | The bytes of a case's session that a grading call reads. |
| `--exposure-log`, `--commit`, `--new-run` | The exposure log (default `$TMPDIR/infra-3231-impl/harness/exposure.log`), a held-out run's commit (default `git rev-parse HEAD`), and the reason for a second held-out run of a cell at one commit (see below). |
| `--figures-only` | Compute the figures from the results the directory holds, and call no model. |
| `--grader-inputs` | The grader's input modes (see "Grader inputs"): `final` (the default), `full` or `trimmed`, then any `,<judgement>=full` or `,<judgement>=trimmed`. |
| `--grader` | The grading model of this run in place of `candidates.yaml`'s grader, with the same fallbacks: a Claude model with a row in `prices.yaml` that is no candidate, not the writer, not the fresh reader and no fallback grader (R2). `run.json` and the figures record it as the grader. |
| `--readers-from` | A run directory whose fresh readers' answers this run grades again, for each case whose text is the same: two runs that compare graders grade the same answers. A reused answer's call is that run's and is not counted again. |
| `--grades-from` | Run directories, comma-separated. For each case whose text is the same, byte for byte, as a text one of those runs graded, this run takes that run's answers and grades that read the same inputs, and calls no model for them (see "Grades of another run"). |
| `--vocabulary` | The `owner-texts.jsonl.gz` of `dataset owner-texts`. The `undefined` judgement then reads the words of each case's text that the owner wrote himself before the case (see "The owner's own words"). |
| `--judgements` | The judgements to give, comma-separated (default all): a measurement of a few, such as `undefined` on the question cases. A run gives no reader answer that no named judgement grades. |
| `--repeatability` | A `repeatability.json` of `harness compare`, whose agreement `scores.md` gives beside each score. |
| `--uncached-input-per-minute` | Hold the run's own model calls to this many uncached input tokens in any one minute (default 0: no pacing). See "Pacing". |

For each case, the judgements are:

- a question case and an answer case: the fresh reader's answer to the person's question, and
  whether it answers the question (`answered`);
- an answer case: the answer of a fresh reader who reads only the first paragraph
  (`first_paragraph`), the first sentence that answers the question (`answer_start`), the text's
  length in words and the words before that sentence;
- a plain case: whether the new text is harder to read than the original (`harder`);
- every case: the phrases that state no fact (`phrases`) and the words and names that neither the
  text nor the record defines (`undefined`), against the record; the facts the new text lost
  (`lost`) and the facts it added that its inputs do not hold (`added`). Under change 1 the text
  is the original, so `lost` and `added` are zero by construction, with no call.

Each judgement reads its subject and the context its input mode gives (`inputs.go`). Full gives
every judgement the person's question, Dispatch's record, the writer's session part and the
writer's answer, as far as the case has them. Trimmed gives each the context it needs:
`answered`, `first_paragraph` and `define` the question and the record (`define` also the writer's
answer); `added` the record, the session and the writer's answer; `undefined` the record; `lost`
and `harder` the two texts alone; `answer_start` the question. `final`, the default, is trimmed for
`answered`, `first_paragraph`, `answer_start` and `harder` and full for the rest (see "Grader
inputs").

The grader's reply gives what it found before its verdict: what the question asks, what the
answer gives, four checks (the answer only says the question again, says only where the answer
is, answers another question, or is wrong by the record), its reason, and its verdict. A check
that is true makes the verdict false. An undefined word that the grader marks defined in the
text or the record, of common meaning, or a name given only as a reference, is no finding; so is
an item whose quote the text does not hold, and a second item of one quote. A lost fact has two
checks: its words state no fact (`states_no_fact`), and the new text states the fact
(`in_new_text`). A lost fact with either check true is no finding. The prompt lists the words
that state no fact: a heading, a label, a word of agreement, a line that says again what another
sentence says, and the reader's own words quoted back. The provider writes a reply's fields in the
order of their names, so each name starts with its place (`1_asked`, ..., `8_answers`). Each grade
keeps the grader's reply as it came.

`phrases` follows the measured definition of a vacuous verb phrase (the rule of `label`'s
rubric), sentence by sentence (`phrases.go`). The candidates are the sentences of the text, its
table cells and list items among them, that hold a form of name, show, pay, carry, hold, land,
arrive, unblock or surface outside inline code; code blocks and quotation lines are left out. The
grader judges each candidate with the two sentences before it and the one after: vacuous, not
vacuous or unresolved, with the literal rewrite. The findings are the vacuous sentences; a text
with no candidate has none, and no call.

A judgement the grader (`claude-opus-5`) refuses on every try goes to the fallback graders of
`candidates.yaml` in order (`claude-opus-4-7`, `claude-opus-4-6`, `claude-sonnet-4-5`), Claude
models that are no candidate and play no other role. The grade keeps each refused call and names
the model that graded it. `--grader` replaces the grader for one run, and keeps the fallbacks.

`run.json` holds the parameters; `cases.jsonl` one line for each case, written as it ends, with
the text the person sees after the change (`text`, the string `text_sha256` hashes: an ask's
question with its options, one line each, as the dataset writes them; any other kind's body), the
readers' answers, each grade, each call's tokens and cost, and the change's trace (see "The
changes"). `cases.jsonl` holds Dispatch text, so the run's directory stays under `$TMPDIR`. A case
whose line holds an error is scored again by the next run. `scores.json` and `scores.md` hold the
figures: for the whole part and for the cases with a session (plan Decision 18), and for each
case group and all groups, the cases by kind and by conversation, each score, the wait, the
change's own model cost per text, and each model's calls, the calls marked for the prompt cache
and those not (see "The prompt cache"), attempts, prompt tokens at the input price, cache write
and cache read tokens, reply tokens and cost.
`scores.md` also gives the uncached input of the run's own calls and its busiest minute (see
"Pacing").

Every share has a 95% interval: a cluster bootstrap over the split groups, by a PCG source
seeded with `--seed`, the 2.5th and 97.5th percentiles of the ratio of the resampled groups'
hits to their cases. Where no case or every case is a hit, the interval is the exact one-sided
95% bound over the split groups, each group one trial (Clopper-Pearson): 1 − 0.05^(1/G) for G
groups. A mean has its bootstrap interval. A score that is zero by construction is marked so.
Under change 1 the wait is 0 and the change calls no model.

`--sample` draws a stratified sample of the part: the strata are the case group and the
conversation flag, and each stratum's target is its share of the size, at least one case. The
split groups are taken in the order of a SHA-256 of the seed and the group's key, each one whole,
and a group is taken when it brings the strata nearer their targets. So a score's interval is
clustered over the same groups on a sample as on the whole part. `run.json` records the sample:
its size, seed, cases, split groups, and each stratum's cases, target and draw.

A run of the held-out part appends one line to the exposure log before it scores a case: the
commit, the time, the changes, the models by role and the output directory, as an absolute path.
The held-out part is scored once at a commit for each cell of the grid: a change with one
candidate, or a change that calls none. A line's cells are its changes with each candidate that
its `models.candidate` names. A lockstep run (see "Lockstep") names all its models there,
comma-separated, so its one line holds a cell for each model.

So a held-out run of several changes, run as several processes at one commit, is one line for
each process, and no line has a reason. For example, change 1, change 2, and each of changes 3,
4, 5, 6 and 7a as a lockstep of the candidates make seven lines at the named commit. Together
they are the one run over the held-out part at that commit.

A run is refused when the log holds a line at the same commit that scored one of its cells,
except in two cases:

- `--new-run '<reason>'` gives the reason for a second run of those cells. The line records the
  reason.
- The run continues that run. The latest line at the commit that scored one of its cells has
  the same `--out` and the same cells. The run scores the cases that hold no result: those with
  no line, and those whose line holds an error. Its line records the time of the line that began
  the run as `continues`, and the results it scores again as `retried`, each model's counted. It
  scores no case that holds a result, and at one commit nothing that decides a score has
  changed, so it is no second exposure. So a run that stopped part way, for a lost pod or a 429
  storm, goes on with the same command.

A continuation is refused once every case of every model holds a result: nothing is left to
continue. A run of those cells then needs `--new-run`.

### Lockstep

`score --change N --models m1,m2,...` scores change N with each of the models in one process.
Each model's run is a lane, in a directory of its own, `<out>/<model>`. A lane's directory holds
what a run of `--model` with that model writes: `cases.jsonl`, `run.json`, `scores.json` and
`scores.md`. Its `run.json` is the same, byte for byte. So `score --model m --out <out>/m`
continues a lane, and `--models` continues a run of `--model` that is in `<out>/<model>`. The
session parts (`session-excerpts.jsonl.gz`) are in `<out>`, once for all the lanes.

The run goes case by case, `--concurrency` cases at once:

1. The change runs for each model that has no result for the case, one model after another,
   through the local Dispatch as in a run of one model. Each request names its model.
2. The grader plans the grading calls of the case's texts of all the models together, before it
   sends the first (see "The prompt cache"). A prefix that the models' calls send alike, such as
   the record and the session part, counts its calls over all of them and is marked.
3. The readers' and the grader's calls of all the models go at once. So the calls of one prefix
   go close together, inside the cache's life of five minutes: the first writes the prefix, and
   the others read it.
4. Each model's result goes to the end of its own `cases.jsonl`.

Each lane's figures are the figures of a run of `--model`: the same function (`computeFigures`)
of the lane's results. Only the facts of the process differ. `scores.json` names the models of
the lockstep as `lockstep`, and the wall time and the uncached input are the whole process's. A
lane's model calls can hold more marked calls than a run of one model, since the plan counts the
calls of every model.

A lockstep run that continues scores, for each model, the cases that model holds no result for.
A case that one model lacks goes to that model alone.

Change 5 and change 6 ask the writer one side question. The question does not depend on the
candidate. The local Dispatch writes it from the ask before it calls the candidate
(`reviewer.RewriteQuestion` in `api/ask_rewrite.go`, `reviewer.DefineQuestion` in
`api/definitions.go`), and the harness writes the component's question from the case in the same
way. Each lane opens an ask of its own, so the questions of two lanes differ only by the ask's id
in the ask's address. So a lockstep run makes the writer's answer once for each case, text and
question, with the ask's id left out, and gives the same answer to every model's run:

- The first lane of the case that asks makes the answer. Each other lane calls no writer. It
  replies the answer to its own ask's question, and records the writer's call with
  `shared_from`: the model of the lane that made it.
- The shared call counts in the lane's change cost per text and in its wait, as in a run of one
  model. Where the local Dispatch waited for the answer, the wait holds the call's time. The call
  is not in the lane's model calls, since the lane did not make it, so the lanes' model tables add
  up to what the process spent. `scores.md` gives the number of shared answers
  (`shared_writer_answers` in `scores.json`).
- A lane whose writer's call failed shares nothing, and the next lane makes its own.
- The process keeps the answers in memory only. A continued run makes the answer again for a case
  that no lane of this process answered.

Where the writer's call depends on the candidate, each model's run makes its own: change 3's
action on the reviewer's findings, change 7a's text written again after the detector's findings,
and, in a combination, a side question about a text that an earlier step made. Change 3's and
change 7a's calls of a case send the same prompt and record for every model, so they count
together and are marked where two calls or more can send them (see "The prompt cache"). A side
answer counts as one call in a lockstep run, so it stays unmarked. Change 2 calls no candidate, so
it takes no `--models`.

### The changes

A change other than 1 is one or more steps a text meets in this order: the written rules (2) and
the writing check alone (7a) when the agent writes, the reviewer (3 or 4) before the text posts, a
person's Rewrite (5) and Define (6) when the person reads it. A combination of `combinations.yaml`
runs its steps in that order, each from the text the one before made; 7b, the check's findings as
the reviewer's inputs, needs 3 or 4. Every model call of the reviewer, Rewrite and Define goes
through the local Dispatch with the candidate in the `Dispatch-Test-Model` header
(`DISPATCH_TEST_MODELS`, `cmd/dispatch/README.md`), so the score is a score of the server's code;
`harness local --test-models` sets that server up. The writer is the writer role's Claude model,
given Dispatch's record of the case, its text, the message Dispatch sent it, and its session up to
the text where the case has one (`writer/writer.md`). Each case's line of `cases.jsonl` holds the
change's own calls, each priced at its model's price (a route's calls by the tokens it answers),
its wait, and its `change_trace`: what each step did. Each review of changes 3 and 4, 7b's
included, is a `review` step that holds what the draft route answered: its `findings`, each with
its `rule_set`, `quote`, `why` and `fix` as the reviewer returns them, and a rewrite that was not
applied (`rewrite`, as the harness holds a text) beside the reason (`not_applied`). Change 7a's
`written_again` step holds the writing check's `findings`: the `rule_set` `structure` or
`detector`, the check's `rule`, the sentence as the `quote`, and the check's `fix`. These fields
are for reading why a change helped or harmed a text; no score reads them.

| Change | What it does |
| --- | --- |
| `2` | The `dispatch` command refuses the write once with its refusal text (a Go copy of `writing-rules.ts`'s `refusalText`, held to it by `TestTheRefusalIsTheCommands`) and the rules the local Dispatch answers for the write's reader (`POST /api/v1/writing-rules`); the writer writes its text again. A document is no write the command refuses. |
| `7a` | The writing check of pull request #3981 alone: its structure rules and its detector, whose reader is the candidate, read the text; their findings go to the writer as #3981's refusal did, and the writer writes its text again. A text with no finding posts as written. |
| `3` | The draft route (`POST /api/v1/reviewer/draft`) reviews the text; the writer gets the findings' frame (`reviewer.FindingsFrame.Body`) and edits, or posts as written with a reason; an edit is reviewed again, up to `--rounds` reviews, after which the last edit posts. With no session the writer is not running (plan Decision 18): the reviewer's rewrite posts under `--unedited rewrite` where the review holds one that is applied, else the text as written. |
| `4` | The draft route's rewrite posts where the review found something and the rewrite keeps what a rewrite keeps. |
| `5` | An ask on an issue is opened in the local Dispatch as the writer's and rewritten by `POST /api/v1/asks/{id}/rewrite`, the case's reader pressing it; the server sends the writer, live in the fake Envoy where the case has a session, one side question, which the harness answers as the writer. The wait runs from the press to the rewrite's end, and the run's calls are read from `ask_rewrites.calls`. A comment, a message or a document is rewritten by the component (`reviewer.Rewrite`) after the same side question, from Dispatch's record as the dataset stored it. |
| `6` | Define (`POST /api/v1/definitions`) of the words the person asked about (those he selected, else those the selection prompt found), for an ask on an issue and a document; the definition from the record, and the writer's answer to the side question beside it. Words in a comment or a message go to the component's Define, with the same side question. It scores the question cases alone. |

A model failure the provider's retry may clear (a provider error, a failed call) is tried again,
up to four times; any other failure of the reviewer posts the text as written, as the server does,
and a failed Rewrite or Define leaves the text as it was, with the failure in the trace.

### grader-test

`grader-test` gives the grader planted outputs, one JSON line each in `--planted`: `id`,
`judgement`, `case_id` (a development case; a held-out case is refused), `planted` (`wrong` or
`right`), `output` (the reader's answer, the new text or the definition), and, as the judgement
needs, `question`, `original`, `marker` (the planted fault a wrong list output holds) and
`words_before` (for `answer_start`). Each judgement needs a wrong and a right output. A correct
grader fails a wrong answer or definition, passes a right one, finds a harder text harder, lists
an item that holds the marker for a wrong list output and nothing for a right one, and finds the
answer within 5 words of `words_before`. It writes `grader-test.json` and `grader-test.md`: for
each judgement, the wrong and the right outputs it marked correctly, then the cost of every call,
refused calls included, with its prompt, cache write and cache read tokens and its calls marked
for the prompt cache and not. The planted outputs are written by hand from development cases and
hold Dispatch text, so they are kept under `$TMPDIR` and never in the repository.
`--grader-inputs` sets the grader's input modes, as `score` takes them. `--grader` names the
grading model, as `score` takes it, except that a candidate may grade here: no candidate wrote a
planted output. The report then says the grader is a candidate, and `grader-test.json` records
the grader and `grader_is_candidate`. `--uncached-input-per-minute` paces the test's calls as it
paces `score`'s (see "Pacing").

### compare and repeatability

`compare` reads two score runs of the same cases and writes, for each judgement, how often they
agree on the cases both graded with no error: the same verdict; for a list judgement, counts
within 1 of each other (beside it, whether both find any, and the mean count difference); for
`answer_start`, both find the answer within 5 words, or neither. Each agreement has the 95%
interval of the scores. Two runs of one grader on the same reader answers (`--readers-from`)
give the grader's repeatability; a full-input and a trimmed run give their agreement. `score
--repeatability` puts a judgement's repeatability beside each of its scores in `scores.md`.

### Grader inputs

`FinalInputs` (`inputs.go`) is trimmed for a judgement whose agreement with the full-input grader,
on a development sample, is no more than 2 points below the full grader's agreement with itself,
or whose planted outputs (`grader-test`) show that the full inputs make errors the trimmed inputs
do not; it is full otherwise. Measured on 2026-11-15 on 312 development cases (`--sample 300`,
seed 3231), each run on the same reader answers:

| Judgement | Full against full | Full against trimmed | Planted outputs wrong: full / trimmed | Mode |
| --- | ---: | ---: | --- | --- |
| `answered` | 96.5% | 93.9% | 1 of 8 in 2 of 4 runs / 1 of 8 in 1 of 6 runs | trimmed |
| `first_paragraph` | 94.7% | 94.1% | none / none | trimmed |
| `undefined` | 86.9% | 79.8% | none / none | full |
| `answer_start` | 95.9% | 88.2% | 1 or 2 of 8 in 4 of 4 runs / none in 6 runs | trimmed |
| `harder` | 100% | 100% | none / none | trimmed |

`lost`, `added` and `define` make no call under change 1, so no agreement was measured, and they
take full inputs. `phrases` reads its candidate sentences alone, in either mode.

### The owner's own words

Without `--vocabulary`, the `undefined` judgement is as it was. It takes a word as known to the
owner only where a person wrote it in the record's thread.

With `--vocabulary <owner-texts.jsonl.gz>`, the judgement also gets, for each case, the words of
the text it grades that the owner wrote himself before the case. Its user message holds them in
the tag `reader_own_words`. Its prompt (`prompts/grade-undefined.md`) says that such a word is not
undefined for him.

- **The time cut.** A word counts when the owner wrote it in a text dated before the case's
  `write_time`, the time the agent wrote the case's text. Where a case has no `write_time`, the
  cut is the time of its agent text. A text of his at that time or after it does not count. So
  his later question about a word of the case does not make the word one he knows. A case with no
  time gets no words.
- **The words.** A word is a run of letters and digits, in lower case, with `-`, `_` and `.` kept
  inside it (`tokens`, which `asked-words` also matches by). A word of one character and a word
  with no letter are left out. So `Train's` matches `train`, and `zork-7` does not match `zork`.
- **The text.** The words are those of the text the person sees after the change, which is the
  text the judgement grades.

`run.json` records the file's SHA-256 as `vocabulary_sha256`. A run that continues in a
directory must read the same file.

### Grades of another run

A change leaves many texts as the agent wrote them, and an earlier run, such as the change-1
baseline, graded those texts. `--grades-from <run dir>[,<run dir>...]` takes those grades and
pays for none of them again.

For each case, the run reads each run of the flag, in the flag's order, for a result whose text
is the same as its own, byte for byte (`text_sha256`). From such a result it takes:

- a fresh reader's answer, when that reader read the same inputs;
- a grade, when the grader read the same inputs.

Each answer and each grade records the SHA-256 of its inputs as `input_sha256`. Two of them read
the same inputs when that SHA-256 is the same. For a grade, it covers the grader and its
fallbacks with their effort, the system prompt, the case's parts the call sends first, the rest of
the user message, the reply's schema and bound, and the session part where the call reads one.
Those parts and the rest of the message hold what the input mode gives the judgement: the texts,
the reader's answer, the question, the record, the writer's answer, the definition and the owner's
own words. So the run takes no grade that was made with another input mode, another prompt,
another layout, another vocabulary or another grader. It gives that judgement itself.

An answer or a grade that the run takes is marked `reused` and keeps the call of the run that
made it. The figures count no call and no cost for it: the model table and the cost in
`scores.md` hold only the calls this run made. The change's own calls are always this run's, but
for a writer's answer that another model's run of a lockstep made (see "Lockstep").
`scores.md` gives the number of answers and grades the run took (`reused_answers` and
`reused_grades` in `scores.json`).

`--grades-from` refuses a directory that holds no result. `run.json` records the directories as
`grades_from`.

`--readers-from` differs. It takes the readers' answers alone, and it does not compare their
inputs, so that another grader grades the same answers.

### phrases-labels and asked-words

Two measurements check the count judgements against references other than the grader's word.

`phrases-labels` grades each write of a `label` round (its `cases.jsonl` and `labels.jsonl`) with
the phrases judgement and places each finding in the labelled sentence that holds its words (code
and addresses as the labels write them, `CODE` and `URL`). It reports the precision (found
sentences labelled vacuous), the recall (vacuous sentences found) and the findings no sentence
holds, each share with its interval over the writes' groups. A run continues the writes it holds
no result for.

`asked-words` reads a score run's `undefined` grades of the question cases and reports the recall
of the words the owner asked about (`owner_reply.verdict.words`) that the agent text uses: an
asked word counts when a finding's words hold its words, or its words hold the finding's.

### local and load

`local` makes a database of its own on the server `--pg-dsn` names (default
`$LEGION_TEST_PG_DSN`), `dispatch_harness_<unix time>` with `createdb`, and starts `bun run
dispatch:local` on it. `--setting NAME=VALUE` and `--pass-through NAME,...` give the server
`DISPATCH_*` settings: `e2e/run-server.sh` passes the names in `DISPATCH_E2E_PASS_THROUGH` to the
server, and with any of them it starts the server with no stand-in model, so a real model group
is not refused beside it. `--database <name> --keep` starts again on a database whose cases are
loaded, with no reset and no seed (`DISPATCH_E2E_KEEP_DATABASE=1`). Once the instance is up,
`local.json` in `--out` holds its URL, its fake Envoy, its database and the settings; the file
goes when the instance stops.

`--test-models` sets the server up for `score`: every candidate a test model at the gateway or
the Foundry account the model flags name (with the Foundry sign-in), each by its name, so a
candidate at an effort is a test model that asks its id for that effort; the reviewer's and
Rewrite and Define's groups at the gateway so their routes answer; and the records job off. A
`--setting` of the same name wins. A change that holds 3 or 4 needs the server's
`DISPATCH_REVIEWER_RULES` to hold `structure` and `detector` exactly when it holds 7b, and
refuses a server that does not; change 2 shows the rule sets of `DISPATCH_WRITING_RULES_EXTRA`.
`local.json` keeps each setting's value, which `score` records in `run.json`.

`load` re-creates each case's record in that instance: the project, an issue with its title,
assigned to a reader of the case's own, each document at the version the record names (one local
version holding that version's Markdown), and the thread as comments, oldest first, each by its
author (an agent's text by its session, a person's by that person's sign-in). A case on no issue
gets its thread as messages in the writer session's conversation, each by the route its author
may use: a person's text is that person's send to the session, and the session's text is its
reply to the person's latest message, or a follow-up under its first reply. A session writes
there only in reply to a person, so where the thread starts with the session's text (a thread a
session opened in production, as an ask on a project document), `load` first sends a message by
the reader that says it is not in the record, and names it in the case's line (`opened`). A text
of another session has no message to answer in that conversation, so it is left out and named in
the case's line (`left_out`). The reader's words are the owner's word records of the verdicts file
written before the case's text (plan Decision 19), never the case's own question. No route of
Dispatch writes a word record, so `load` writes those rows into the database that `local` made. A
document the instance refuses (`INVALID_MARKDOWN`, for a block that points at a text of production
it does not hold) is left out and named in the case's line. `loaded.jsonl` holds where each record
stands, and a load continues the cases it holds no line for. Write times and authors differ from
production, so the grader reads the record from the dataset, never from the local instance.
`load --sample N --sample-seed S --groups G` loads the cases
`score --sample N --sample-seed S --groups G` scores.
