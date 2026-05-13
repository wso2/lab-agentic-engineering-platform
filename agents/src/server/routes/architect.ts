import type express from "express";
import { streamText, stepCountIs } from "ai";
import { createAnthropic } from "@ai-sdk/anthropic";
import { config } from "../../shared/config.js";
import { ArchitectInput } from "../../agents/architect/schema.js";
import {
  systemPrompt,
  buildUserPrompt,
} from "../../agents/architect/prompt.js";
import { DesignDoc } from "../../agents/architect/doc.js";
import {
  buildTools,
  type SseSink,
  type FinalizeResolver,
} from "../../agents/architect/tools.js";
import {
  resolveAnthropicKey,
  AnthropicKeyError,
} from "../../shared/anthropic-key-resolver.js";
import { getOrgId } from "../../middleware/org-id.js";

function writeFrame(res: express.Response, frame: unknown): void {
  res.write(`data: ${JSON.stringify(frame)}\n\n`);
}

export function registerArchitect(app: express.Express) {
  app.post("/v1/agents/architect", async (req, res) => {
    const parsed = ArchitectInput.safeParse(req.body);
    if (!parsed.success) {
      res.status(400).json({ error: parsed.error.format() });
      return;
    }

    const orgId = getOrgId(res);
    let anthropicApiKey: string;
    try {
      anthropicApiKey = (await resolveAnthropicKey(orgId)).key;
    } catch (err) {
      if (err instanceof AnthropicKeyError) {
        res.status(err.status).json({ error: err.message, code: err.code });
        return;
      }
      throw err;
    }
    const anthropic = createAnthropic({ apiKey: anthropicApiKey });

    console.log(
      `[architect] streaming orgId=${orgId} (spec ${parsed.data.spec.length} chars, incremental=${parsed.data.previousDesign ? "yes" : "no"})`,
    );

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

    // Prevent OC API gateway idle-timeout from dropping the SSE connection
    // while waiting for Anthropic. SSE comments are forwarded by the BFF and
    // ignored by browser SSE parsers.
    const keepAlive = setInterval(() => {
      if (!res.writableEnded) res.write(": keep-alive\n\n");
    }, 15_000);

    const sse: SseSink = {
      send(event, data) {
        if (res.writableEnded) return;
        writeFrame(res, { type: `data-${event}`, data });
      },
      isClosed() {
        return res.writableEnded;
      },
    };

    const finalizer: FinalizeResolver = {
      finalized: false,
      resolve: () => {
        // Once finalize() emits data-finish, we no longer need the model to
        // keep talking. Aborting the stream tears down the SDK loop cleanly.
        abortController.abort();
      },
    };

    const doc = DesignDoc.fromPrevious(parsed.data.previousDesign);
    const tools = buildTools(doc, sse, finalizer, parsed.data.wireframes ?? {});

    try {
      const result = streamText({
        model: anthropic(config.model),
        system: systemPrompt,
        prompt: buildUserPrompt(parsed.data, doc),
        tools,
        // 64-step runaway-loop guard. finalize() is the real terminator.
        stopWhen: stepCountIs(64),
        abortSignal: abortController.signal,
        onError: ({ error }) => {
          console.error("[architect] streamText error:", error);
        },
        onFinish: (ev) => {
          console.log(
            `[architect] finish=${ev.finishReason} steps=${ev.steps?.length ?? 0} in=${ev.usage?.inputTokens ?? 0} out=${ev.usage?.outputTokens ?? 0}`,
          );
        },
      });

      // Drive the loop. Tools emit SSE events as side effects; the textStream
      // chunks are uninteresting to us (no UI surface for the model's natural
      // language; the design doc never shows it).
      for await (const _chunk of result.textStream) {
        if (res.writableEnded) break;
      }

      // Loop ended without finalize() succeeding. Could be: stepCountIs hit,
      // model gave up, or upstream error. Emit error so BFF doesn't write.
      if (!finalizer.finalized && !res.writableEnded) {
        writeFrame(res, {
          type: "error",
          errorText:
            "architect agent ended without calling finalize — design not produced",
        });
      }

      if (!res.writableEnded) res.write("data: [DONE]\n\n");
    } catch (err) {
      // Ignore aborts triggered by our own finalize() resolver.
      const aborted =
        abortController.signal.aborted &&
        (err instanceof Error
          ? err.name === "AbortError" || /aborted/i.test(err.message)
          : false);
      if (aborted && finalizer.finalized) {
        if (!res.writableEnded) res.write("data: [DONE]\n\n");
      } else {
        console.error("[architect] pipe error:", err);
        if (!res.writableEnded) {
          writeFrame(res, {
            type: "error",
            errorText: err instanceof Error ? err.message : String(err),
          });
        }
      }
    } finally {
      clearInterval(keepAlive);
      if (!res.writableEnded) res.end();
    }
  });
}
