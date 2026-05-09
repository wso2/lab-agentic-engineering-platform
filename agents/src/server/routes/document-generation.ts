import type express from "express";
import { z } from "zod";
import { streamText } from "ai";
import { anthropic } from "@ai-sdk/anthropic";
import { config } from "../../shared/config.js";
import { getDocumentGenerationSkill } from "../../skills/document-generation/index.js";

const RequestBody = z.object({
  sources: z.record(z.string(), z.string()).optional(),
  prompt: z.string().optional(),
});

/**
 * Generic document-generation route. Path: `/v1/agents/document-generation/{skillId}`.
 *
 * The skillId selects a registered DocumentGenerationSkill (see
 * skills/document-generation/index.ts). The body carries optional source
 * files (filename → content) and an optional user prompt. Response is the
 * AI SDK UI Message Stream over SSE.
 */
export function registerDocumentGeneration(app: express.Express) {
  app.post(
    "/v1/agents/document-generation/:skillId",
    async (req, res) => {
      const skillId = req.params.skillId;
      const skill = getDocumentGenerationSkill(skillId);
      if (!skill) {
        res.status(404).json({ error: `unknown skill: ${skillId}` });
        return;
      }

      const parsed = RequestBody.safeParse(req.body);
      if (!parsed.success) {
        res.status(400).json({ error: parsed.error.format() });
        return;
      }

      const userPrompt = skill.buildUserPrompt({
        sources: parsed.data.sources ?? {},
        prompt: parsed.data.prompt,
      });

      console.log(
        `[document-generation/${skillId}] streaming (user-prompt ${userPrompt.length} chars, sources=${Object.keys(parsed.data.sources ?? {}).length})`,
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

      const result = streamText({
        model: anthropic(config.model),
        system: skill.systemPrompt,
        prompt: userPrompt,
        abortSignal: abortController.signal,
        onError: ({ error }) => {
          console.error(`[document-generation/${skillId}] streamText error:`, error);
        },
        onFinish: (ev) => {
          console.log(
            `[document-generation/${skillId}] finish=${ev.finishReason} in=${ev.usage.inputTokens ?? 0} out=${ev.usage.outputTokens ?? 0}`,
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
        console.error(`[document-generation/${skillId}] pipe error:`, err);
        const errorChunk = {
          type: "error",
          errorText: err instanceof Error ? err.message : String(err),
        };
        res.write(`data: ${JSON.stringify(errorChunk)}\n\n`);
      } finally {
        res.end();
      }
    },
  );
}
