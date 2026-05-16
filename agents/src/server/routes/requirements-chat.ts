import type express from "express";
import { streamText, stepCountIs } from "ai";
import { createAnthropic } from "@ai-sdk/anthropic";
import { config } from "../../shared/config.js";
import {
  RequirementsChatInput,
  RequirementsDoc,
  systemPrompt,
  buildUserPrompt,
  buildTools,
  type SseSink,
  type FinalizeResolver,
} from "../../agents/requirements-chat/index.js";
import {
  resolveAnthropicKey,
  AnthropicKeyError,
} from "../../shared/anthropic-key-resolver.js";
import { getOrgId } from "../../middleware/org-id.js";

function writeFrame(res: express.Response, frame: unknown): void {
  res.write(`data: ${JSON.stringify(frame)}\n\n`);
}

export function registerRequirementsChat(app: express.Express) {
  app.post("/v1/agents/requirements-chat", async (req, res) => {
    const parsed = RequirementsChatInput.safeParse(req.body);
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

    const fileCount = Object.keys(parsed.data.files).length;
    console.log(
      `[requirements-chat] streaming orgId=${orgId} mode=${parsed.data.mode} message=${parsed.data.message.length}c files=${fileCount}`,
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
        abortController.abort();
      },
    };

    const doc = new RequirementsDoc(parsed.data.files);
    const tools = buildTools(doc, sse, finalizer, parsed.data.mode);

    try {
      const result = streamText({
        model: anthropic(config.model),
        system: systemPrompt,
        prompt: buildUserPrompt(
          parsed.data.message,
          parsed.data.history,
          parsed.data.files,
          parsed.data.mode,
        ),
        tools,
        // Per the design: stepCountIs(64) runaway guard; finish() is the
        // intended terminator.
        stopWhen: stepCountIs(64),
        abortSignal: abortController.signal,
        onError: ({ error }) => {
          console.error("[requirements-chat] streamText error:", error);
        },
        onFinish: (ev) => {
          console.log(
            `[requirements-chat] finish=${ev.finishReason} steps=${ev.steps?.length ?? 0} in=${ev.usage?.inputTokens ?? 0} out=${ev.usage?.outputTokens ?? 0}`,
          );
        },
      });

      // Forward free-form model text to the client as text-delta frames so
      // the chat can render the assistant's thinking-out-loud commentary.
      for await (const chunk of result.textStream) {
        if (res.writableEnded) break;
        if (chunk.length > 0) {
          writeFrame(res, { type: "text-delta", delta: chunk });
        }
      }

      if (!finalizer.finalized && !res.writableEnded) {
        writeFrame(res, {
          type: "error",
          errorText:
            "requirements-chat agent ended without calling finish",
        });
      }

      if (!res.writableEnded) res.write("data: [DONE]\n\n");
    } catch (err) {
      const aborted =
        abortController.signal.aborted &&
        (err instanceof Error
          ? err.name === "AbortError" || /aborted/i.test(err.message)
          : false);
      if (aborted && finalizer.finalized) {
        if (!res.writableEnded) res.write("data: [DONE]\n\n");
      } else {
        console.error("[requirements-chat] pipe error:", err);
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
