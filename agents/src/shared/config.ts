export const config = {
  model: process.env.AGENT_MODEL || "claude-sonnet-4-5",
  maxSteps: parseInt(process.env.AGENT_MAX_STEPS || "10", 10),
  logLevel: process.env.LOG_LEVEL || "info",
} as const;
