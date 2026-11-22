# Style of a skill

Write a skill that an agent scans for its next action, in the style that alpha-set and beta-set share.

**Done when:** a fresh reader given only the skill and this file finds each rule below followed.

Each rule has an example and a line of either set that shows it. `as:` is alpha-set's `skills/`. `bs:` is beta-set's repository, under `skills/` unless the path starts `.agents/`.

## The parts of a SKILL.md, in order

The table puts rules 1, 2, 6 and 10 in order.

| Part | Holds |
| --- | --- |
| `# Title`, then one or two sentences | The goal: what is true when the skill has worked |
| `**Done when:**` | The check that ends the skill |
| `## Step 1: <verb> <object>`, or `## 1. <name>` | The procedure, as numbered steps |
| A table | Facts that share their attributes, such as situation and action |
| `## References` | Each reference with its condition, where no step names it |

## Rules

1. **Open with the goal in one or two sentences, then a `**Done when:**` line.**
   - Example: `Rotate the build key with no outage.` then `**Done when:** the old key is revoked and each job runs with the new one.`
   - Source: as `alpha-skill-one/SKILL.md:10` ("**Core rule:** evidence before claims."); bs `beta-skill-one/SKILL.md:11` ("**Done when:** the review is complete").
2. **Write a procedure as numbered steps, each starting with its name.**
   - Example: `1. **Reproduce.** Run the failing command.`
   - Source: as `alpha-skill-two/SKILL.md:52` ("1. **Read the error**"); bs `beta-skill-two/SKILL.md:37` ("1. **Mechanism.** Say what the thing does").
3. **Put one rule in a list item, starting with the rule or with a bold label.** Its detail follows: how, when, the exception, and a reason as rule 5 allows.
   - Example: `- **Read before you edit.** Open the file and its callers first.`
   - Source: as `alpha-skill-three/SKILL.md:26` ("**Guard:** the check holds inside a submodule too."); bs `beta-skill-one/SKILL.md:37` ("- **Report only; never push.**").
4. **Write short sentences, one idea each.** Split a sentence at its second rule.
   - Example: `Write the test first. Watch it fail.`, not `Write the test first and watch it fail, which proves the test can fail.`
   - Source: as `alpha-skill-one/SKILL.md:10` ("Write the test first. Watch it fail."); bs `.agents/skills/beta-skill-work/SKILL.md:33` ("Write one idea per sentence.").
5. **Give a reason only where the rule would look arbitrary:** one clause after the rule, or a `**Why:**` line, that names the failure the rule prevents.
   - Example: `Run the tests before you push.` stands bare. `Pin the version: a floating tag changes between two runs.` needs its reason.
   - Source: bs `.agents/skills/beta-skill-work/SKILL.md:28` ("**A line earns its place** by stating a constraint the agent can check."); as `alpha-skill-three/SKILL.md:88` ("**Why:** stops a commit of the scratch tree.").
6. **Put facts that share their attributes in a table:** situation and action, code and meaning, claim and evidence.
   - Example: `| Exit code | Do |` with the rows `| 0 | Go on |` and `| 2 | Fix the arguments |`.
   - Source: as `alpha-skill-three/SKILL.md:144-146` ("| Situation | Action |").
7. **Give one example for each shape of command or output, with its parameters.** Add a bad example only where the bad form is the common one.
   - Example: `run-check <skill dir>...`, once, not once for each skill.
   - Source: as `writing-alpha/SKILL.md:113` ("**One good example beats many weak ones**"); bs `.agents/skills/beta-skill-work/SKILL.md:32` ("write one recipe with parameters").
8. **Write in the imperative, to "you".** Describe the system only where the agent acts on the fact.
   - Example: `Run the migration.`, not `The migration should be run.`
   - Source: as `alpha-skill-one/SKILL.md:10` ("Write the test first."); bs `beta-skill-two/SKILL.md:18` ("Resolve the scope in this order:").
9. **Tell no story:** put a date, an incident, a link or a design history in the commit message, because the agent reads a story as an instruction and misses its rule.
   - Example: `Run the migration before the deploy.`, not `One deploy failed because the migration had not run.`
   - Source: as `writing-alpha/SKILL.md:315-317` (a story given as the bad example).
10. **Name each reference at the step that needs it, with its condition.**
    - Example: `**Read references/testing.md now** when the change alters what an agent does.`
    - Source: as `alpha-skill-two/SKILL.md:216` ("When you write a test, read testing.md"); bs `beta-skill-two/SKILL.md:48` ("**Read `references/look.md` now**").
