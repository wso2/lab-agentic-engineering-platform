import type express from "express";
import { z } from "zod";
import { streamText } from "ai";
import { anthropic } from "@ai-sdk/anthropic";
import { config } from "../../shared/config.js";
import { systemPrompt } from "../../agents/business-analyst/prompt.js";

const RequestBody = z.object({ prompt: z.string().min(1) });

export function registerBusinessAnalyst(app: express.Express) {
  app.post("/v1/agents/business-analyst", async (req, res) => {
    const parsed = RequestBody.safeParse(req.body);
    if (!parsed.success) {
      res.status(400).json({ error: parsed.error.format() });
      return;
    }

    console.log(
      `[business-analyst] streaming (prompt ${parsed.data.prompt.length} chars)`,
    );

    res.writeHead(200, {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache, no-transform",
      Connection: "keep-alive",
      "x-vercel-ai-ui-message-stream": "v1",
    });
    res.flushHeaders?.();

    // Abort the upstream model call if the client disconnects so we don't
    // burn tokens on a response no one is reading.
    const abortController = new AbortController();
    res.on("close", () => {
      if (!res.writableEnded) abortController.abort();
    });

    const result = streamText({
      model: anthropic(config.model),
      system: systemPrompt,
      prompt: parsed.data.prompt,
      abortSignal: abortController.signal,
      onError: ({ error }) => {
        console.error("[business-analyst] streamText error:", error);
      },
      onFinish: (ev) => {
        console.log(
          `[business-analyst] finish=${ev.finishReason} in=${ev.usage.inputTokens ?? 0} out=${ev.usage.outputTokens ?? 0}`,
        );
      },
    });

    try {
      for await (const chunk of result.toUIMessageStream()) {
        if (abortController.signal.aborted) break;
        res.write(`data: ${JSON.stringify(chunk)}\n\n`);
      }
      res.write("data: [DONE]\n\n");
    } catch (err) {
      console.error("[business-analyst] pipe error:", err);
      const errorChunk = {
        type: "error",
        errorText: err instanceof Error ? err.message : String(err),
      };
      res.write(`data: ${JSON.stringify(errorChunk)}\n\n`);
    } finally {
      res.end();
    }
  });
}
