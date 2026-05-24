// Materialises the per-task AgentSkills plugin tree under
// <workspace>/.asdlc/skills-plugin/.
//
// Layout (matches docs/design/skills-system.md > "Coding agent"):
//
//   .asdlc/skills-plugin/
//     .claude-plugin/
//       plugin.json                                 # {"name":"asdlc-task-skills","version":"1.0"}
//     skills/
//       builtin-api-management/
//         SKILL.md                                  # rewritten name: builtin-api-management
//         references/<file>.md                      # optional
//       builtin-go/
//         SKILL.md
//       custom-payments-pci-handling/
//         SKILL.md
//
// All kinds land in one plugin directory. The materialisation prefix
// (`builtin-`, `custom-`, `imported-`) is applied to both the directory
// name AND the `name:` frontmatter field; the original name is preserved
// under metadata.asdlc.canonical-name.

import fs from "node:fs";
import path from "node:path";
import type { SkillResolution } from "./skills_pull.js";

export interface MaterializeResult {
  pluginDir: string;
  builtinNames: string[]; // for the SDK `skills:` preload array
}

export async function materializeSkills(
  workspace: string,
  skills: SkillResolution[],
): Promise<MaterializeResult | null> {
  if (skills.length === 0) {
    return null;
  }
  const pluginDir = path.join(workspace, ".asdlc", "skills-plugin");
  const claudePluginDir = path.join(pluginDir, ".claude-plugin");
  const skillsDir = path.join(pluginDir, "skills");

  await fs.promises.mkdir(claudePluginDir, { recursive: true });
  await fs.promises.mkdir(skillsDir, { recursive: true });

  await fs.promises.writeFile(
    path.join(claudePluginDir, "plugin.json"),
    JSON.stringify({ name: "asdlc-task-skills", version: "1.0" }, null, 2) + "\n",
    { mode: 0o644 },
  );

  const builtinNames: string[] = [];

  for (const sk of skills) {
    const skillDir = path.join(skillsDir, sk.materializedName);
    await fs.promises.mkdir(skillDir, { recursive: true });

    const rewritten = rewriteSkillFrontmatter(sk.skillMd, sk.materializedName);
    await fs.promises.writeFile(path.join(skillDir, "SKILL.md"), rewritten, { mode: 0o644 });

    if (sk.references && Object.keys(sk.references).length > 0) {
      for (const [refPath, refBody] of Object.entries(sk.references)) {
        if (!refPath.startsWith("references/")) continue;
        if (refPath.includes("..")) continue; // safety
        const fullPath = path.join(skillDir, refPath);
        await fs.promises.mkdir(path.dirname(fullPath), { recursive: true });
        await fs.promises.writeFile(fullPath, refBody, { mode: 0o644 });
      }
    }

    if (sk.kind === "builtin") {
      builtinNames.push(sk.materializedName);
    }
  }

  return { pluginDir, builtinNames };
}

// Rewrite the `name:` field in the SKILL.md frontmatter to the
// materialised name; preserve everything else verbatim. Also adds
// metadata.asdlc.canonical-name with the original name so any tooling
// that wants to find the source skill can.
//
// Quick frontmatter-only parse: we expect every SKILL.md to start with
// `---\n`. If it doesn't (defensive), bail out and write the body
// untouched.
export function rewriteSkillFrontmatter(skillMD: string, materializedName: string): string {
  const trimmed = skillMD.trimStart();
  if (!trimmed.startsWith("---")) {
    return skillMD; // no frontmatter — leave alone
  }
  const afterFirst = trimmed.indexOf("\n");
  if (afterFirst < 0) return skillMD;
  const endIdx = trimmed.indexOf("\n---", afterFirst);
  if (endIdx < 0) return skillMD;

  const fm = trimmed.slice(afterFirst + 1, endIdx);
  const body = trimmed.slice(endIdx + "\n---".length).replace(/^\r?\n/, "");

  const canonicalMatch = fm.match(/^name:\s*(.+)$/m);
  const canonicalName = canonicalMatch ? canonicalMatch[1].trim() : materializedName;

  // Replace existing name: line; if not present, prepend one.
  let newFm = fm;
  if (canonicalMatch) {
    newFm = newFm.replace(/^name:\s*.+$/m, `name: ${materializedName}`);
  } else {
    newFm = `name: ${materializedName}\n` + newFm;
  }

  // Stamp metadata.asdlc.canonical-name. If a metadata block exists,
  // merge into it; otherwise append a fresh one. Keeping this simple —
  // we don't claim YAML correctness for every edge case, just the
  // common shape our bootstrap writes.
  if (/^metadata:\s*$/m.test(newFm)) {
    const asdlcBlock = newFm.match(/^(\s+)asdlc:\s*$/m);
    if (asdlcBlock) {
      // Nested asdlc block already present — insert canonical-name as a
      // child so it isn't dropped.
      const childIndent = asdlcBlock[1] + "  ";
      newFm = newFm.replace(
        /^(\s+asdlc:\s*)$/m,
        `$1\n${childIndent}canonical-name: "${canonicalName}"`,
      );
    } else {
      newFm = newFm.replace(/^metadata:\s*$/m, `metadata:\n  asdlc:\n    canonical-name: "${canonicalName}"`);
    }
  } else {
    newFm = newFm + `\nmetadata:\n  asdlc:\n    canonical-name: "${canonicalName}"`;
  }

  return `---\n${newFm}\n---\n\n${body}`;
}
