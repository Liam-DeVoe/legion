/**
 * Resolves when `work` settles, fulfilled or rejected, or once `ms` have passed, naming which came
 * first; never rejects. The timer is cleared either way, so a wait that ended on its fact holds
 * nothing open. Every wait of the channel server's way out goes through this, so each has a bound
 * of its own, and the tests' record names each with the bound it armed (`./channel-record`).
 */
export async function settledOrGivenUp(
  work: Promise<unknown>,
  ms: number,
): Promise<"fact" | "bound"> {
  const { promise, resolve } = Promise.withResolvers<"bound">()
  const timer = setTimeout(() => resolve("bound"), ms)
  try {
    return await Promise.race([
      work.then(
        () => "fact" as const,
        () => "fact" as const,
      ),
      promise,
    ])
  } finally {
    clearTimeout(timer)
  }
}
