import type { AnswerAskInput } from "../../api/client";
import type { AskAnswer, AskV2 } from "../../api/types";

export function answerAskInput(
  ask: Pick<AskV2, "edited_at">,
  selected: string[],
  text: string,
  replacing?: AskAnswer
): AnswerAskInput {
  const trimmedText = text.trim();
  return {
    selected,
    ...(trimmedText === "" ? {} : { text: trimmedText }),
    expected_edited_at: ask.edited_at,
    ...(replacing === undefined ? {} : { expected_answer_at: replacing.at }),
  };
}
