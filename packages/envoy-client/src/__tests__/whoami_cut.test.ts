import { afterAll, beforeAll, describe, expect, setSystemTime, test } from "bun:test";
import {
  ASK_OPENED,
  callsAtCutProblems,
  leftOutParts,
  READ_FORMS,
  startAsOfStandIn,
} from "./as-of-stand-in";

// A token that carries a cut reads Dispatch at that cut, whatever `--as-of` the command is given
// (dispatch://INFRA-4620, decision 6). Whoami names the cut, and each of the seven read commands,
// run with no `--as-of` and with a later one, sends the token's cut as `as_of` to each route of
// kind "answers", calls no route that holds no history, names the token's cut in its `Left out
// at` line, and counts from it. The test's clock stands years after both times.

/** The cut the token carries. */
const TOKEN_CUT = "2026-11-05T08:00:00Z";
/** A later `--as-of`, a week after the token's cut, which the token's cut replaces. */
const LATER = "2026-11-12T08:00:00Z";
const CLOCK = "2031-06-01T00:00:00Z";

const standIn = startAsOfStandIn("whoami-cut");

beforeAll(() => {
  setSystemTime(new Date(CLOCK));
});

afterAll(() => {
  setSystemTime();
  standIn.stop();
});

/** One form of each of the seven read commands. */
const COMMANDS = [
  READ_FORMS.issue,
  READ_FORMS.blocks,
  READ_FORMS.search,
  READ_FORMS.answers,
  READ_FORMS.openAsks,
  READ_FORMS.inbox,
  READ_FORMS.results,
];

/** What a run under the token's cut printed that only a count from that cut prints: the answers'
 *  window, each open ask's age and the Inbox's left-out Later band. */
function countedFrom(argv: readonly string[], stdout: string): string[] {
  const opened = Date.parse(ASK_OPENED);
  const hours = Math.floor((Date.parse(TOKEN_CUT) - opened) / 3_600_000);
  const expected: Record<string, readonly string[]> = {
    answers: [
      `1 item people wrote to this session in the 72 hours before ${TOKEN_CUT}, newest first, word for word:`,
      "The words of an hour before.",
    ],
    "open-asks": [`- ${Math.floor(hours / 24)}d ${hours % 24}h · `],
    inbox: [`Later left out at ${TOKEN_CUT}`],
  };
  return (expected[argv[0] ?? ""] ?? []).filter((words) => !stdout.includes(words));
}

describe("a token's cut replaces --as-of", () => {
  for (const argv of COMMANDS) {
    for (const asOf of [[], ["--as-of", LATER]]) {
      test(`dispatch ${[...argv, ...asOf].join(" ")} under a token cut at ${TOKEN_CUT}`, async () => {
        const run = await standIn.run([...argv, ...asOf], { whoamiCut: TOKEN_CUT });
        expect({
          code: run.code,
          calls: callsAtCutProblems(run, TOKEN_CUT),
          namesTheCut: leftOutParts(run.stdout, TOKEN_CUT) !== undefined,
          namesTheLaterTime: run.stdout.includes(LATER),
          notCountedFromTheCut: countedFrom(argv, run.stdout),
        }).toEqual({
          code: 0,
          calls: [],
          namesTheCut: true,
          namesTheLaterTime: false,
          notCountedFromTheCut: [],
        });
      });
    }
  }
});
