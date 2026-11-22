# consumernames

`github.com/sjawhar/envoy/consumernames` gives each JetStream consumer a physical name of its own at
each creation, behind a logical name its readers know, so no consumer name is ever created twice.

nats-server returns from a consumer delete while the old consumer's writer can still write its
state file, and a create of the same name writes the same file. The late write can then fail the
create (10012) or give the new consumer the old one's state, so after a restart the new consumer
skips what it was created to deliver. A fresh name sends every late write to a directory no
consumer reads.

## Names

| Family | Logical name | Fixed name (the fixed mode) | Physical name of each creation (the fresh mode) |
| --- | --- | --- | --- |
| Session consumer | the session id | `s-<session id>` | `session~<nuid>` |
| The listener's own durable | `listener-<machine id>` | the logical name | `listener-<machine id>~<nuid>` |
| Dispatch's durables | `delivery-events`, `delivery-jobs` | the logical name | `<logical>~<nuid>` |
| A Legion daemon's durables | `legion-go-<project>-<kind>` | the logical name | `<logical>~<nuid>` |

- `FreshName(prefix)` is `<prefix>~<nuid>`. A `~` is in no fixed name, so no fresh name is a fixed one.
- `Config.Prefix(logical)` is the part before the `~`: `session` for a session consumer, the logical
  name itself for a durable. `LogicalOf` cuts a durable's physical name at its last `~`. It does not
  give a session's logical name, which is the consumer's metadata (below).
- **Metadata.** Every fresh consumer carries `envoy.logical=<logical name>` (`LogicalMetadataKey`).
  A session consumer also carries `envoy.session_id=<session id>`.
- **Logical names.** `CheckKey` refuses a logical name before anything is written: an empty one,
  one holding `.`, `*`, `>`, `/`, `\`, white space or a control character (`ErrInvalidKey`), and
  one of 254 bytes or more, whose fixed name the server refuses (`ErrKeyTooLong`, with the server's
  own text). The listener's durable is kept under `DurableKey(durable)`: `durable.` and the name,
  with each byte outside `[-_a-zA-Z0-9]` written `=` and two hex digits. It is a key no session id
  can be.

## The record

Each logical name has one selection record (`Record`), kept with conditional writes on its
revision:

| Field | Meaning |
| --- | --- |
| `key` | the key the record is stored under (below); a read refuses a record carrying any other |
| `current` | the selected physical name, or empty |
| `op` | the operation in flight: `create`, `replace` or `delete`, with its owner, candidate, old name, reservation time and `floor_recorded` |
| `start` | the saved start, written before any destructive step |
| `last_floor` | the current consumer's last ack floor, with the name and the stream identity it was read on |
| `retired` | names never selected again; only these are ever deleted by a cleanup |
| `restore` | a rollback in progress |

- **Lifetime.** A record is never deleted and never expires. Publishing a new `current` clears
  `last_floor`. A replaced predecessor whose delete the server confirmed leaves `retired` again;
  one whose delete failed stays there for the cleanup.
- **Where records live, and their keys.** A store reads and writes only its own family's
  sentinel and records:
  - The session family keeps its records in the KV bucket `envoy_session_consumers`
    (`SessionBucket`, `NewKVStore(request)`): one key per session id, the listener durables under
    `DurableKey`, and the sentinel under `envoy.bucket` (`SentinelKey`). Each read takes the key's
    last message from the stream's leader, so a delete or purge marker is seen as one.
  - Dispatch and the daemons keep theirs in the Postgres table `intake_consumers`
    (`NewPGStore(family, db)`). Several families can share one database, as several projects'
    daemons do. So a record of logical name L in family F is the row `F/L` (`PGKey`), and F's
    sentinel is the row `envoy.sentinel/F` (`PGSentinelKey`): for example
    `daemon/<project>/legion-go-<project>-github` and `envoy.sentinel/daemon/<project>`, or
    `dispatch/delivery-events` and `envoy.sentinel/dispatch`. `List` returns only the family's
    records, never a sentinel. `NewPGStore` and `DaemonFamily(project)` refuse any family but
    `session`, `dispatch` and `daemon/<project>` with a project that is not empty and holds no `/`,
    white space or control character (`ErrInvalidFamily`, naming it): a store of `daemon` alone
    would list every project's records, and one of `daemon/p/b` would write into `daemon/p`'s.
- **The sentinel** (`StoreState`) names its store's family (`session`, `dispatch` or
  `daemon/<project>`) and holds the cutover marker (`cutover`: empty, `done` or `rolled_back`), the
  cutover boundary B (`boundary`), the stream's identity, and a recovery's inputs (`replay_from`,
  `orphans`, the backup's sha256, the hold). Each store's `WriteSentinel` writes its own family
  itself when the state names none, and refuses a state that names another (`ErrSentinelFamily`,
  naming both), so no caller writes another family's sentinel, or one its store cannot read.
- **Strict read.** A value is a record only when it is one JSON object of the record's fields and
  nothing after it, carrying the key it was read under, with every field well formed. Any principal
  with a JetStream API grant can have the server store its answer under any key of the bucket
  through a request's reply subject, and one with a direct get can copy another key's message
  there whole. So any other value is `ErrMalformedRecord` for that key alone. Under the sentinel's
  key any value but the store's own exact sentinel is `ErrMalformedSentinel`: a sentinel that
  names another family or none (`ErrSentinelFamily`), one under another key, and one with a field
  this build does not know among them. It is never `ErrNoSentinel`, which is an absent sentinel
  alone, so a creator in the fixed mode that reads `ErrNoSentinel` as no marker reads a malformed
  one as a failed read.

  `DecodeKVValue(key, data) (record, state, isSentinel, err)` is the session bucket's decode, the
  one `KVStore`'s `Read` and `State` call: under `envoy.bucket` the session family's sentinel
  (`isSentinel` true), under any other key that key's record, and anything else
  `ErrMalformedSentinel` or `ErrMalformedRecord`, naming the key. A reader of the bucket's raw
  messages, such as an operator's walk of the stream, decodes each value with it as the store
  does. It reads values only: a message whose `KV-Operation` header marks a delete or purge is the
  reader's to answer, as `ErrRecordDeleted`, or `ErrNoSentinel` under `envoy.bucket`, before it
  decodes anything.
- **The family fence.** A record's current, retired names and candidate must be
  `<Prefix(logical)>~<nuid>` names of its own key. Its operation's old name may also be the key's
  fixed name, which the cutover replaces. Anything else is `ErrMalformedRecord`, naming the field.
  `Names.Check(logical, record)` is that fence, the one the creator applies to every record it
  reads. Its nil says only that the record's names are of its key's family. The record must come
  from a store read or `DecodeKVValue`, which check its key and fields, and a name it gives must
  pass `Names.Owns` (the consumer's own metadata) before anything reads, binds, updates or deletes
  it: every session's prefix is `session`, so a record of one session can name another session's
  consumer and pass the fence. A reader that deletes what a record names calls `Names.Cleanup`,
  which does all three.

  The prefix and the fixed name come from one table in this package (`Naming`, `FamilyNaming`):
  `SessionNaming()` (`session~<nuid>`, fixed `s-<session id>`), `DurableNaming()` for a key
  `DurableKey` made (`<durable>~<nuid>`, fixed the durable's name), and `LogicalNaming()` for
  Dispatch's and a daemon's durables (`<logical>~<nuid>`, fixed the logical name). Every creator
  builds its `Config.Prefix` and `Config.Fixed` from it, and `CheckFamily(family, logical,
  record)` is `Check` by the family's own naming, for a reader with no creator's `Config`, such as
  the operator's module, which cannot import the creators' packages. Its nil means what `Check`'s
  does.
- **The owner check.** A consumer a record names is used only when the consumer's own metadata
  names the key (`Names.Owns`, `Config.Metadata`). It is checked before every read, adoption and
  delete of such a name, so a record that names another key's consumer is malformed, and nothing
  reads, binds, updates, adopts or deletes that consumer for it.

## The protocol (`Names`)

- **Step 0, first in every call:** an open operation is settled. Its candidate, if the server
  holds it, is published (adopted). If not, once the operation is 2 s old (`Config.SettleAge`), the
  candidate is retired and a fresh one is created at the saved start.
- **Adoption.** A candidate this call did not see created (Step 0, or a create whose answer was
  lost) is published only when it is as the family creates it (`ErrUnexpectedConsumer` otherwise,
  naming it, and the operation stays open):
  - its start is the one the operation reserved: the native policy of a native first create, else
    a start by sequence no later than the saved start;
  - every setting no update changes is as the family's create gives it (`Config.Adopt`;
    `bus.Client.ConsumerAdoptable` compares the server's own answer with the family's whole create
    configuration). That means explicit acks, no start time, instant replay, the stream's storage,
    no deliver group, no flow control, no heartbeat, no pause, neither direct nor sourcing. For a
    session it also means a pull consumer at the server's default max waiting; for the listener's
    durable, a push consumer delivering to a reply inbox. The comparison is an allowlist: a setting
    the server answers that this build does not know is refused too.
  - A paused candidate is refused, never corrected. nats-server v2.15.1 sets a consumer's own
    pause back on every create or update of a consumer that exists (server/jetstream_api.go
    :5261-5264, server/jetstream_cluster.go :12678-12680), so only a `CONSUMER.PAUSE` request
    changes it, and no listener grant holds that request. Its operation stays open, as for any
    refused candidate, until an operator repairs the key.

  Every setting an update changes is then written back to the family's create (the filters, max
  batch, headers only, ack wait and the others; `bus.CorrectedConfig`), in place, before anything is
  delivered. A read of the candidate that fails is returned as itself, not as a refusal.
- **Create** (no current): the start is the record's `start`, else the family's first-create
  rule, and for a session key with no record no later than the cutover boundary B. The call
  reserves the operation and the start in one conditional write, creates the candidate once, then
  publishes it. `Config.NativeFirstCreate` keeps a family's native first create (the daemon's
  deliver policy new) for a logical name that never existed.
- **Replace:** reserve, create at the saved start, publish, retire and delete the predecessor.
- **Delete:** reserve, read and save the floor (one that cannot be read stops the call), delete,
  clear the current.
- **At a full cap:** the retired names the server holds are deleted, and the create is tried once
  more. The cap is never updated (`Config.NoDelete` answers at once for a family that cannot delete).
- **A missing current:** a record whose current the server no longer holds is created again at its
  `last_floor` plus one when that floor was read from that consumer on this stream, else at its
  saved start. With neither, the call is refused (`ErrNoSafeStart`). It never searches the
  server's other consumers and never uses a first-create rule.
- **Ambiguous answers:** an unanswered create is checked with an info: present means adopt (as
  above), missing means retire. No name is sent to the server's create twice.

## The two modes

Each process reads `ENVOY_CONSUMER_NAMES` (`ModeEnvVar`) once at its start, exactly
(`ParseMode`):

- **`fixed`** (also when unset): the family's code before this package, with its fixed names,
  unchanged. A process changes mode only by a restart with a new value. A reconnect never changes
  it, and nothing a store holds selects it. A creator in the fixed mode reads its store's cutover
  marker and refuses to run fixed names over a store the cutover converted. The listener does
  this per NATS connection; see `packages/envoy/README.md`.
- **`fresh`**: the protocol above. Every call is refused (`ErrCutoverNotRun`) until the store's
  sentinel says `cutover = done` (`Admit`).
- Any other value, a padded one or another case among them, refuses the start, naming the
  setting and the value.

## Missing selection state

In the fresh mode lost state is a refusal at the first call, never a fall back to fixed names. A
missing marker cannot tell a store before its cutover from one whose state was lost after it.

| State | Error |
| --- | --- |
| the bucket or table is missing | `ErrNoStore` (only the server's own "stream not found", 10059; a read no server answers is a failed read) |
| the sentinel is absent (no row; in the bucket, no message or a delete or purge marker) | `ErrNoSentinel` |
| the sentinel key holds anything but the store's own exact sentinel: malformed, another family's, under another key, or with a field this build does not know | `ErrMalformedSentinel` (`ErrSentinelFamily` too for another family's) |
| the cutover has not run, or was rolled back | `ErrCutoverNotRun` |
| a key whose last message is a KV delete or purge marker | `ErrRecordDeleted` |
| a key holding something other than its own record | `ErrMalformedRecord` |
| a missing current with no safe start | `ErrNoSafeStart` |
| an open create's candidate that another principal made first, with the key's own metadata and a setting the family's create never gives and no update changes | `ErrUnexpectedConsumer`, naming the candidate and the setting. A session's registrations are refused for that session alone. The listener's own durable stops the listener at every start (`subscribe refused, shutting down`, as a refused durable does today). An accepted residual: it fails closed. The remedy is the operator's `envoy-infra consumer-names replace --listener` for the durable, and a replay of the key (`consumer-names replay`) for a session |
| in the fixed mode, a cutover marker read answered "stream not found" (10059) by a cluster member that has not yet applied the bucket's create | read as no bucket, so no marker. A stated limit: the session bucket is created, recreated and restored only inside a writer hold, while every creator is held, and the hold's restart round waits for the stream before it releases them, so no fixed-mode reader meets that answer outside a hold |

Two more stated limits. A hub whose JetStream limits set `max_request_batch` has the server fill
`max_batch` on every pull consumer again after each correction, so the listener would send an
update on every `Ensure` and never keep a listed consumer; envoy-infra's `gcp:ClusteredNatsHub`
has no such input, and the deployment sets none. A refused paused candidate stays on the server
once the operator's replay has overwritten its operation, named by no record and holding a slot
of the stream's consumer limit, until the cutover hold's streams census removes it as an orphan.

A key never written, while the sentinel is present, is a new name, and the family's first-create
rule applies. A family can refuse that too (`Config.MissingRecord`): Dispatch's two durables, and a
daemon's checkpoint with no record. `IsSelectionStateLost` reports the lost-state errors together.

**What this does not stop.** A NATS user that receives every reply on the account (`_INBOX.>`, as
the Legion daemon's does) can read a pending candidate's name and the messages the listener pulls;
that is the user's grant, not this package's. A login that shares a family's Postgres database can
write rows under another family's keys, and a well-formed record there is read (its consumer still
passes the owner check only with the key's own metadata); that is the database's access, not this
package's.

## Recovery

Recovery is an operator's step inside a writer hold, never a rebuild from the stream's consumers:
no call of this package reads another consumer to guess a start.

- **A lost or malformed key:** the operator writes the key's record with a start and no current
  (a replay). The next call creates a fresh name at that start.
- **A lost bucket:** the operator creates it again, from a backup or with a replay boundary.
  While `replay_from` is set, a key with no record starts there. The sentinel lists the orphans
  the recovery found, and the listener's sweep ends replay mode once none of them exists
  (`KVStore.EndReplay`).
- **A malformed sentinel:** the operator rewrites it. Until then the fresh mode refuses every call,
  and the fixed mode reads again rather than run fixed names over a marker it cannot read.
