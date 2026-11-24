import { z } from "zod";
import {
  CommentDeliverySchema,
  CommentEventPayloadSchema,
  CommentMentionSchema,
} from "./dispatch-api-comments";
import { DispatchEventSchema } from "./dispatch-api-events";
import { DELIVERY_CAPABILITIES } from "./dispatch-api-limits";
import { MessageEventPayloadSchema } from "./dispatch-api-messages";

/**
 * A targeted delivery as the host that receives it reads it: the message or comment Dispatch
 * aimed at one session, and the delivery line naming the attempt it answers. Each is the
 * inbound payload reading with the fields a targeted frame always carries made required, so a
 * host knows it can answer the attempt before it hands the frame to its agent.
 */

export const DispatchTargetedResourceIDSchema = z.uuid();

export const DispatchTargetedMessagePayloadSchema = MessageEventPayloadSchema.extend({
  id: DispatchTargetedResourceIDSchema,
  issue_key: z.string().nullable(),
  author: z.object({ kind: z.string(), id: z.string() }),
  body: z.string(),
  target: z.string(),
  in_reply_to: z.string().nullable(),
  // The broadcast a frame's message is one recipient's copy of, as the frame claims it: a frame is
  // untrusted, so neither its value nor its absence proves anything.
  broadcast_id: z.string().nullish(),
  deliveries: z.array(z.unknown()),
  created_at: z.string(),
});

export const DispatchTargetedCommentPayloadSchema = CommentEventPayloadSchema.extend({
  id: DispatchTargetedResourceIDSchema,
  issue_key: z.string().nullable(),
  artifact_id: z.string().nullable(),
  author: z.object({ kind: z.string(), id: z.string() }),
  body: z.string(),
  reply_to: z.string().nullable(),
  ask_id: z.string().nullable(),
  mentions: z.array(CommentMentionSchema),
  deliveries: z.array(CommentDeliverySchema),
  created_at: z.string(),
  artifact_name: z.string(),
});

export const DispatchTargetedMessageDeliverySchema = z.object({
  attempt: z.number().int().positive(),
  mode: z.enum(DELIVERY_CAPABILITIES),
});

export const DispatchTargetedCommentDeliverySchema = DispatchTargetedMessageDeliverySchema.extend({
  comment_id: DispatchTargetedResourceIDSchema,
  target: z.string(),
});

export const DispatchTargetedDeliverySchema = z.union([
  DispatchTargetedCommentDeliverySchema,
  DispatchTargetedMessageDeliverySchema,
]);

/** Whether a targeted delivery line names a comment's attempt (a mention) rather than a message's. */
export function isCommentTargetedDelivery(
  delivery: z.infer<typeof DispatchTargetedDeliverySchema>
): delivery is z.infer<typeof DispatchTargetedCommentDeliverySchema> {
  return "comment_id" in delivery;
}

/**
 * A targeted Dispatch frame, `{event, delivery}`, as a host reads it: the event's header, the
 * delivery line, and a payload complete for the event's kind - a message.created event with a
 * message's delivery line, or a comment.created event with a comment's. The refinement picks the
 * payload schema by the event's type and the delivery line's kind; JSON Schema cannot say that,
 * so the edge listener's Go reader of this shape (envoy internal/contracts, ParseDispatchFrame)
 * makes the same choice by hand over the shapes `schemas/dispatch-frame.schema.json` carries.
 */
export const DispatchTargetedFrameSchema = z
  .object({
    event: DispatchEventSchema,
    delivery: DispatchTargetedDeliverySchema,
  })
  .superRefine(({ event, delivery }, context) => {
    if (
      event.type === "message.created" &&
      !isCommentTargetedDelivery(delivery) &&
      DispatchTargetedMessagePayloadSchema.safeParse(event.payload).success
    ) {
      return;
    }
    if (
      event.type === "comment.created" &&
      isCommentTargetedDelivery(delivery) &&
      DispatchTargetedCommentPayloadSchema.safeParse(event.payload).success
    ) {
      return;
    }
    context.addIssue({
      code: "custom",
      message:
        event.type === "comment.created"
          ? "targeted delivery requires a complete comment.created event"
          : "targeted delivery requires a complete message.created event",
    });
  });

/**
 * A targeted frame that fails DispatchTargetedFrameSchema but still names the message or comment
 * it carries, so a host can refuse that attempt by name.
 */
export const RecoverableDispatchDeliveryFailureSchema = z
  .object({
    event: z
      .object({
        type: z.enum(["message.created", "comment.created"]),
        issue_key: z.string().nullable(),
        payload: z.object({ id: z.string(), body: z.string().optional() }).passthrough(),
      })
      .passthrough(),
    delivery: DispatchTargetedDeliverySchema,
  })
  .refine(
    ({ event, delivery }) =>
      event.type === "comment.created"
        ? isCommentTargetedDelivery(delivery)
        : !isCommentTargetedDelivery(delivery) &&
          DispatchTargetedResourceIDSchema.safeParse(event.payload.id).success,
    { message: "delivery resource must match its event type" }
  );

/**
 * What a host makes of a Dispatch envelope's payload, `value` being it after JSON.parse: an
 * `event` it shows (a targeted frame DispatchTargetedFrameSchema reads, or an event
 * DispatchEventSchema reads), a `recovered` targeted frame it refuses by name
 * (RecoverableDispatchDeliveryFailureSchema), or `raw`, a payload it reads as neither. The edge
 * listener passes a Dispatch frame only when its payload is an `event` here (ParseDispatchFrame).
 */
export type DispatchPayloadKind = "event" | "recovered" | "raw";

export function dispatchPayloadKind(value: unknown): DispatchPayloadKind {
  if (typeof value === "object" && value !== null && "delivery" in value) {
    if (DispatchTargetedFrameSchema.safeParse(value).success) return "event";
    if (RecoverableDispatchDeliveryFailureSchema.safeParse(value).success) return "recovered";
    return "raw";
  }
  return DispatchEventSchema.safeParse(value).success ? "event" : "raw";
}
