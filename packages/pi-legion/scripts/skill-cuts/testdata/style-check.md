## Style

Write a skill an agent scans, not a document it reads. Each rule below names its source in the style notes.

**Order of a SKILL.md** (N1, N2, N3):

| Part | Holds |
|---|---|
| `# Title`, then 1–2 sentences | The goal: what is true when the skill has worked |
| `**Done when:**` | The check that ends the skill |
| `## Mode` | Only when the skill has modes |
| `## Step 1: <verb> <object>` … | The procedure |
| `## Quick reference` | A table of situation → action, when there are 4 or more |
| `## Red flags` | Only for a rule agents skip under pressure |
| `## References` | Each file with its condition, if the steps did not name it |

**Rules:**

- **One rule per bullet or numbered line** (N4). Start it with the rule in bold, or with a bold label; the detail follows.
- **Short sentences, one idea each** (N5). Split a sentence at its second rule. A line over 40 words holds two rules.
- **Reason only where the rule would look arbitrary** (N6). Give it as a clause after the rule or as `**Why:**`. A rule that is obvious stands bare.
- **Tables for parallel facts** (N7). Two or more cases with the same attributes go in a table: code → action, claim → evidence.
- **One example per shape** (N8). Give a fenced command or template once. Pair a wrong form with the right one only where the wrong form is common.
- **Imperative, to "you"** (N9). Describe the system only where the agent acts on the fact.
- **No story** (N10). A date, an incident or a design history goes in the commit message.
- **Reference at the step** (N11). "**Read `references/x.md` now**" at the step that needs it.
- **Description** (N12). Start with "Use when" and give the triggers, never the steps. Name a sibling skill for a near miss.
- **No capitals for force** (N13). Bold marks a rule's label, not its strength.

**Check before you commit:**

- The goal line and the `**Done when:**` line exist.
- No paragraph holds more than one rule.
- Each list of 4 or more condition → action pairs is a table.
