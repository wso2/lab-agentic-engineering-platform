import type express from "express";
import { z } from "zod";
import { streamObject, streamText } from "ai";
import { createAnthropic, type AnthropicProvider } from "@ai-sdk/anthropic";
import { config } from "../../shared/config.js";
import {
  resolveAnthropicKey,
  AnthropicKeyError,
} from "../../shared/anthropic-key-resolver.js";
import { getOrgId } from "../../middleware/org-id.js";
import {
  PlanArraySchema,
  PlanItemSchema,
  TechLeadPlanInput,
  TechLeadDetailInput,
  type PlanItem,
} from "../../agents/tech-lead/schema.js";
import {
  planSystemPrompt,
  detailSystemPrompt,
  buildPlanUserPrompt,
  buildDetailUserPrompt,
} from "../../agents/tech-lead/prompt.js";
import {
  validatePlan,
  type DiffContext,
  type PlanItemWithTempId,
} from "../../agents/tech-lead/validator.js";

function writeFrame(res: express.Response, frame: unknown): void {
  if (res.writableEnded) return;
  res.write(`data: ${JSON.stringify(frame)}\n\n`);
}

function setupSse(res: express.Response): {
  abortController: AbortController;
  keepAlive: NodeJS.Timeout;
} {
  res.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache, no-transform",
    Connection: "keep-alive",
    "x-vercel-ai-ui-message-stream": "v1",
  });
  res.flushHeaders?.();

  const abortController = new AbortController();
  res.on("close", () => {
    if (!res.writableEnded) abortController.abort();
  });

  const keepAlive = setInterval(() => {
    if (!res.writableEnded) res.write(": keep-alive\n\n");
  }, 15_000);

  return { abortController, keepAlive };
}

// =============================================================================
// Phase 1 — Plan
// =============================================================================

// Wire shape extension — the validator wants pre-computed diff context for
// incremental coverage rules. BFF computes this; the route just shuttles it.
const PlanRequestBody = TechLeadPlanInput.extend({
  diff: z
    .object({
      added: z.array(z.string()),
      contractAffectedModified: z.array(z.string()),
      removed: z.array(z.string()),
    })
    .optional(),
});

export function registerTechLeadPlan(app: express.Express) {
  app.post("/v1/agents/tech-lead/plan", async (req, res) => {
    const parsed = PlanRequestBody.safeParse(req.body);
    if (!parsed.success) {
      res.status(400).json({ error: parsed.error.format() });
      return;
    }

    const orgId = getOrgId(res);
    let anthropic: AnthropicProvider;
    try {
      anthropic = createAnthropic({ apiKey: (await resolveAnthropicKey(orgId)).key });
    } catch (err) {
      if (err instanceof AnthropicKeyError) {
        res.status(err.status).json({ error: err.message, code: err.code });
        return;
      }
      throw err;
    }

    console.log(
      `[tech-lead/plan] orgId=${orgId} mode=${parsed.data.mode} components=${parsed.data.slimDesign.length} existing=${parsed.data.existingTasks?.length ?? 0}`,
    );

    const { abortController, keepAlive } = setupSse(res);
    const diffCtx: DiffContext | undefined = parsed.data.diff;

    try {
      const result = streamObject({
        model: anthropic(config.model),
        system: planSystemPrompt,
        prompt: buildPlanUserPrompt(parsed.data),
        schema: PlanArraySchema,
        abortSignal: abortController.signal,
        onError: ({ error }) => {
          console.error("[tech-lead/plan] streamObject error:", error);
        },
      });

      // Seal-rule emitter (design §4): emit element i when partial array length
      // ≥ i+2 (so element i is no longer the trailing one being typed). Every
      // sealed element is `safeParse`-d for the full PlanItem shape.
      const sealed: PlanItemWithTempId[] = [];
      let sealedThrough = -1;

      for await (const partial of result.partialObjectStream) {
        if (res.writableEnded) break;
        if (!Array.isArray(partial)) continue;
        const sealedTo = partial.length - 2;
        for (let i = sealedThrough + 1; i <= sealedTo; i++) {
          const parsedItem = PlanItemSchema.safeParse(partial[i]);
          if (!parsedItem.success) {
            writeFrame(res, {
              type: "error",
              data: {
                scope: "plan",
                code: "malformed-plan-item",
                index: i,
                issues: parsedItem.error.format(),
              },
            });
            return;
          }
          const tempId = `p-${i}`;
          const item: PlanItemWithTempId = { tempId, ...parsedItem.data };
          sealed.push(item);
          sealedThrough = i;
          writeFrame(res, {
            type: "data-plan-item",
            data: item,
          });
        }
      }

      // Stream ended — flush the trailing element (if any).
      const final = await result.object;
      for (let i = sealedThrough + 1; i < final.length; i++) {
        const parsedItem = PlanItemSchema.safeParse(final[i]);
        if (!parsedItem.success) {
          writeFrame(res, {
            type: "error",
            data: {
              scope: "plan",
              code: "malformed-plan-item",
              index: i,
              issues: parsedItem.error.format(),
            },
          });
          return;
        }
        const tempId = `p-${i}`;
        const item: PlanItemWithTempId = { tempId, ...parsedItem.data };
        sealed.push(item);
        writeFrame(res, { type: "data-plan-item", data: item });
      }

      // Validator pass.
      const issues = validatePlan({
        items: sealed,
        design: parsed.data.slimDesign,
        existingTasks: parsed.data.existingTasks ?? [],
        mode: parsed.data.mode,
        diff: diffCtx,
      });
      if (issues.length > 0) {
        writeFrame(res, {
          type: "error",
          data: { scope: "plan", issues },
        });
        return;
      }

      writeFrame(res, {
        type: "data-plan-complete",
        data: { items: sealed },
      });
    } catch (err) {
      const aborted =
        abortController.signal.aborted &&
        (err instanceof Error
          ? err.name === "AbortError" || /aborted/i.test(err.message)
          : false);
      if (!aborted) {
        console.error("[tech-lead/plan] error:", err);
        writeFrame(res, {
          type: "error",
          data: {
            scope: "plan",
            errorText: err instanceof Error ? err.message : String(err),
          },
        });
      }
    } finally {
      clearInterval(keepAlive);
      if (!res.writableEnded) {
        res.write("data: [DONE]\n\n");
        res.end();
      }
    }
  });
}

// =============================================================================
// Phase 2 — Detail (parallel streamText fan-out)
// =============================================================================

const DETAIL_CONCURRENCY = parseInt(
  process.env.TECH_LEAD_PHASE2_CONCURRENCY || "4",
  10,
);
const DELTA_COALESCE_MS = 250;

export function registerTechLeadDetail(app: express.Express) {
  app.post("/v1/agents/tech-lead/detail", async (req, res) => {
    const parsed = TechLeadDetailInput.safeParse(req.body);
    if (!parsed.success) {
      res.status(400).json({ error: parsed.error.format() });
      return;
    }

    const orgId = getOrgId(res);
    let anthropic: AnthropicProvider;
    try {
      anthropic = createAnthropic({ apiKey: (await resolveAnthropicKey(orgId)).key });
    } catch (err) {
      if (err instanceof AnthropicKeyError) {
        res.status(err.status).json({ error: err.message, code: err.code });
        return;
      }
      throw err;
    }

    console.log(
      `[tech-lead/detail] orgId=${orgId} tasks=${parsed.data.items.length} concurrency=${DETAIL_CONCURRENCY}`,
    );

    const { abortController, keepAlive } = setupSse(res);
    const data = parsed.data;

    try {
      // Tiny semaphore — keeps a bounded number of streamText calls in flight
      // without pulling in p-limit as a dep.
      const queue = [...data.items];
      const tasks: Promise<void>[] = [];

      async function runNext(): Promise<void> {
        const item = queue.shift();
        if (!item) return;
        await runDetailForItem(
          res,
          data.projectName,
          data.spec,
          item,
          abortController,
          anthropic,
        );
        await runNext();
      }

      const initial = Math.min(DETAIL_CONCURRENCY, queue.length);
      for (let i = 0; i < initial; i++) tasks.push(runNext());
      await Promise.all(tasks);
    } catch (err) {
      const aborted =
        abortController.signal.aborted &&
        (err instanceof Error
          ? err.name === "AbortError" || /aborted/i.test(err.message)
          : false);
      if (!aborted) {
        console.error("[tech-lead/detail] fan-out error:", err);
        writeFrame(res, {
          type: "error",
          data: {
            scope: "detail",
            errorText: err instanceof Error ? err.message : String(err),
          },
        });
      }
    } finally {
      clearInterval(keepAlive);
      if (!res.writableEnded) {
        res.write("data: [DONE]\n\n");
        res.end();
      }
    }
  });
}

async function runDetailForItem(
  res: express.Response,
  projectName: string,
  spec: string,
  item: TechLeadDetailInput["items"][number],
  abortController: AbortController,
  anthropic: AnthropicProvider,
): Promise<void> {
  if (res.writableEnded) return;

  const prompt = buildDetailUserPrompt(projectName, spec, item);
  let fullBody = "";
  let pendingDelta = "";
  let lastFlush = Date.now();

  const flush = () => {
    if (pendingDelta.length === 0) return;
    writeFrame(res, {
      type: "data-task-body-delta",
      data: { taskId: item.taskId, delta: pendingDelta },
    });
    pendingDelta = "";
    lastFlush = Date.now();
  };

  try {
    const result = streamText({
      model: anthropic(config.model),
      system: detailSystemPrompt,
      prompt,
      abortSignal: abortController.signal,
      onError: ({ error }) => {
        console.error(`[tech-lead/detail ${item.taskId}] streamText error:`, error);
      },
    });

    for await (const chunk of result.textStream) {
      if (res.writableEnded) return;
      pendingDelta += chunk;
      fullBody += chunk;
      const now = Date.now();
      if (now - lastFlush >= DELTA_COALESCE_MS) flush();
    }
    flush();

    if (!res.writableEnded) {
      writeFrame(res, {
        type: "data-task-body-complete",
        data: { taskId: item.taskId, body: fullBody },
      });
    }
  } catch (err) {
    const aborted =
      abortController.signal.aborted &&
      (err instanceof Error
        ? err.name === "AbortError" || /aborted/i.test(err.message)
        : false);
    if (aborted) return;
    console.error(`[tech-lead/detail ${item.taskId}] error:`, err);
    writeFrame(res, {
      type: "error",
      data: {
        scope: "detail",
        taskId: item.taskId,
        errorText: err instanceof Error ? err.message : String(err),
      },
    });
  }
}
