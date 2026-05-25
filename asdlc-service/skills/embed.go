// Package skills embeds the bundled built-in SKILL.md files into the BFF
// binary so SkillBootstrap.Run() can UPSERT them into the `skills` table
// at startup without depending on a checked-out source tree.
//
// See docs/design/skills-system.md > "Bootstrap".
package skills

import "embed"

//go:embed builtin/*/SKILL.md
var BuiltinFS embed.FS
