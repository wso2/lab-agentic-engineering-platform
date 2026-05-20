export const instructions = `You have the ability to explore and understand codebases systematically.

When exploring a codebase:
1. Start by listing the root directory to understand the project structure.
2. Look for entry points: package.json, main/index files, configuration files.
3. Read key configuration files to understand the tech stack and dependencies.
4. Follow imports from entry points to trace the architecture.
5. Use searchFiles to find specific patterns, function definitions, or usages.

When asked to understand a specific part of the codebase:
- Read the relevant files directly rather than guessing about their contents.
- Trace dependencies and imports to understand how components connect.
- Look for tests to understand expected behavior.

Always report what you found with file paths so the user can verify.`;
