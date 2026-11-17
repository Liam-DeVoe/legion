// The host surfaces this extension prints through, each passing the scrub of the secrets this
// process holds (`@legion/contracts/held-secrets`): the host's logger, a session's notices, and
// what a tool answers or throws, which the model reads. The rules of `@legion/contracts/log-text`
// name nothing a peer or a setting wrote; this is the backstop under them, for a server at the
// wrong URL that echoes what it was sent inside a text the extension prints as guidance. A text
// that holds no held secret reaches the host as it was.
import { scrub, scrubError } from "@legion/contracts/held-secrets";

/** `value` with its strings scrubbed: a string, or the strings of a plain object or array one
 *  level down (a logger's fields). Anything else is passed on as it is. */
function scrubbedArgument(value: unknown): unknown {
  if (typeof value === "string") return scrub(value);
  if (value instanceof Error) return scrubError(value);
  if (Array.isArray(value))
    return value.map((item) => (typeof item === "string" ? scrub(item) : item));
  if (
    typeof value === "object" &&
    value !== null &&
    Object.getPrototypeOf(value) === Object.prototype
  ) {
    const out: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value)) {
      out[key] =
        typeof item === "string" ? scrub(item) : item instanceof Error ? scrubError(item) : item;
    }
    return out;
  }
  return value;
}

/** `target` with each of its methods named in `methods` scrubbing its arguments first. */
function scrubbingMethods<T extends object>(target: T, methods: ReadonlySet<string | symbol>): T {
  return new Proxy(target, {
    get(object, key) {
      const value = Reflect.get(object, key, object) as unknown;
      if (typeof value !== "function") return value;
      if (!methods.has(key)) return value.bind(object);
      return (...args: unknown[]) => value.apply(object, args.map(scrubbedArgument));
    },
  });
}

const LOG_METHODS = new Set<string | symbol>(["trace", "debug", "info", "warn", "error", "log"]);

/** The host's logger, scrubbing every message and field it is handed. */
export function scrubbedLogger<T extends object>(logger: T): T {
  return scrubbingMethods(logger, LOG_METHODS);
}

/** One scrubbing view per host context, so a context the extension keeps is one object. */
const VIEWS = new WeakMap<object, object>();

/** A host context whose `ui.notify` scrubs the notice; everything else is the context's own. */
export function scrubbedContext<T>(context: T): T {
  if (typeof context !== "object" || context === null) return context;
  const kept = VIEWS.get(context);
  if (kept !== undefined) return kept as T;
  const view = new Proxy(context, {
    get(object, key) {
      const value = Reflect.get(object, key, object) as unknown;
      if (key === "ui" && typeof value === "object" && value !== null) {
        return scrubbingMethods(value, new Set(["notify", "setStatus"]));
      }
      return typeof value === "function" ? value.bind(object) : value;
    },
  });
  VIEWS.set(context, view);
  return view;
}

/** A tool's answer with the text of each content part scrubbed. */
function scrubbedToolResult(result: unknown): unknown {
  if (typeof result !== "object" || result === null || !("content" in result)) return result;
  const { content } = result as { content?: unknown };
  if (!Array.isArray(content)) return result;
  return {
    ...result,
    content: content.map((part: unknown) =>
      typeof part === "object" && part !== null && "text" in part && typeof part.text === "string"
        ? { ...part, text: scrub(part.text) }
        : part
    ),
  };
}

/**
 * The host API `pi`, whose event handlers are handed a context that scrubs its notices, and whose
 * tools answer and throw scrubbed text. Everything else is the host's own, and its type is too.
 */
export function scrubbedHost<T extends object>(pi: T): T {
  return new Proxy(pi, {
    get(object, key) {
      const value = Reflect.get(object, key, object) as unknown;
      if (key === "on" && typeof value === "function") {
        return (event: string, handler: (...args: unknown[]) => unknown) =>
          value.call(object, event, (...args: unknown[]) =>
            handler(...args.map((arg, at) => (at === 1 ? scrubbedContext(arg) : arg)))
          );
      }
      if (key === "registerTool" && typeof value === "function") {
        return (tool: { execute?: (...args: unknown[]) => unknown }) => {
          const execute = tool.execute;
          if (typeof execute !== "function") return value.call(object, tool);
          return value.call(object, {
            ...tool,
            execute: async (...args: unknown[]) => {
              const last = args.length - 1;
              const scoped = args.map((arg, at) => (at === last ? scrubbedContext(arg) : arg));
              try {
                return scrubbedToolResult(await execute.apply(tool, scoped));
              } catch (error) {
                throw scrubError(error);
              }
            },
          });
        };
      }
      return typeof value === "function" ? value.bind(object) : value;
    },
  });
}
