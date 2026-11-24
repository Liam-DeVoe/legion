# The client's exit status

Read this when `agent-secrets` exits with no `agent-secrets: <names> from <source>` line on stderr: the status is then the client's.

| Status | Means | Do |
| --- | --- | --- |
| 1 | A refusal or a failure that the message names | |
| 1, `UNKNOWN_SECRET` | Final | Do not ask again, and do not try a name of your own |
| 1, `PENDING_VALUE` | Its owner is storing the value now | Ask again in a few minutes |
| 1, `REQUEST_PENDING` | A waiting request of this session for the same name, whatever its target | Ask again once it is decided |
| 2 | A usage error: no `AGENT_SECRETS_URL`, no `--`, a `--target` refused, or a removed GitHub flag (it names its replacement); or a broker too old to confirm `--target`. Nothing ran | |
| 75 | Still undecided when `--wait` runs out, and nothing ran. The message gives the `agent-secrets status <request_id>` that reads it | |
| 77 | Denied; the message gives the broker's reason when it recorded one | Final, except in the next two rows: ask for none of it again, and try no name of your own |
| 77, `ask again with --reason` | | Ask again with `--reason` |
| 77, `requester-group-unread` | | Ask again later |
