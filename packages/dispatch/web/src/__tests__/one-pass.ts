import { countReaderSteps } from "../features/refs/reader-steps";

/**
 * How many steps past four times its steps on a text a reader may take on a text four times as
 * long. A reader of one pass takes `a·n + b` steps on a text of `n`, which at `4n` is within
 * `4·(a·n + b)` wherever `b` is not below zero, and past it by `-3b`, a few steps whatever `n`
 * is, where a loop skips a few characters it was handed. A reader that rereads its text takes
 * about 16 times its steps on four times the text, far past this at the lengths read here.
 */
export const ONE_PASS_SLACK = 16;

/** The steps of this folder's text readers (`features/refs/reader-steps.ts`) that `read` takes
 *  on a text of `n` and on one of four times `n`. */
export function stepsAtNAndFourN(
  read: (n: number) => void,
  n: number
): { atN: number; atFourN: number } {
  return {
    atFourN: countReaderSteps(() => read(4 * n)),
    atN: countReaderSteps(() => read(n)),
  };
}
