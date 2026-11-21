---
title: Oh My Pi plugin interface
parent: legion
depends_on: [envoy-client]
paths: [packages/pi-shared]
---
The private workspace package both Oh My Pi plugins bundle (`@legion/pi-shared`): the versioned in-process interface the Envoy plugin publishes and the Legion plugin reads, and the modules both entries inline — the role-claim bridge, the injected-user-turn record, the subagent check, the host types, the jj session attribution overlay every harness's pane commits through — with the real-binary test harness and the shared skills-guard and cross-import rules.

`src/approval-message.ts` holds how both plugins' `context` handlers read one model request. It names the approval message's custom type (`APPROVAL_MESSAGE_TYPE`), which pi-legion places and pi-envoy puts its answers message before, and cuts a request before its trailing run of approval messages (`splitTrailingApproval`). It also holds the checks that tell a side turn (`isSideTurn`), the handoff prompt (`isHandoffPrompt`) and a compaction in progress (`sessionCompacting`, read off the live session in the host's `AgentRegistry`) from a request of the conversation's own loop, the key a message is matched by (`messageKey`), and the reader of a message's first text (`leadingText`). A caller passes the host's `AgentRegistry` in, so the module imports host types only and loads where the host package does not resolve, as in each plugin's unit tests. `src/custom-entry.ts` holds the readers of a session's custom entries: every entry of one type (`customEntries`), and the newest one this build can read (`lastCustomEntry`).
