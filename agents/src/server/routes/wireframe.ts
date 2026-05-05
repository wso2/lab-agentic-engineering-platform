import type express from "express";
import { generateObject } from "ai";
import { anthropic } from "@ai-sdk/anthropic";
import { config } from "../../shared/config.js";
import { WireframeInput, WireframeOutput } from "../../agents/wireframe/schema.js";
import { systemPrompt, buildUserPrompt } from "../../agents/wireframe/prompt.js";

export function registerWireframe(app: express.Express) {
  app.post("/v1/agents/wireframe", async (req, res) => {
    const parsed = WireframeInput.safeParse(req.body);
    if (!parsed.success) {
      res.status(400).json({ error: parsed.error.format() });
      return;
    }

    console.log(
      `[wireframe] generating (spec ${parsed.data.spec.length} chars, incremental=${parsed.data.previousSpec ? "yes" : "no"})`,
    );

    try {
      const result = await generateObject({
        model: anthropic(config.model),
        system: systemPrompt,
        prompt: buildUserPrompt(parsed.data),
        schema: WireframeOutput,
      });

      console.log(
        `[wireframe] generated ${result.object.content.length} chars of HTML`,
      );

      res.json(result.object);
    } catch (err) {
      console.error("[wireframe] error:", err);
      res.status(500).json({
        error: err instanceof Error ? err.message : String(err),
      });
    }
  });
}
