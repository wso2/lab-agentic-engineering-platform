# Scenario 2: Specification Management

## Description
User creates and manages specifications that drive the entire development lifecycle.

## User Scenarios

### 1. Write Spec Manually
- User navigates to project → Spec tab
- User writes specification in a markdown editor
- User can preview rendered markdown
- User saves the spec (creates a version)

### 2. Generate Spec from Prompt
- User navigates to project → Spec tab
- User enters a natural language prompt describing what they want to build
- System sends prompt to AI agent
- AI generates a structured specification in markdown
- User reviews, edits if needed, and saves

### 3. View Spec Version History
- User views the version history of a spec
- User can compare versions (diff view)
- User can revert to a previous version

### 4. Approve Spec
- User reviews the spec and approves it
- This transitions the spec status from draft → approved
- Approval enables the Design phase

### 5. Generate Wireframe from Spec
- From the Spec tab, user clicks "Generate Wireframe"
- System reads the current `.asdlc/spec.md` and (when a previous tagged spec exists) passes it as delta context
- AI agent (`agents-service` `/v1/agents/wireframe`) returns a complete HTML document using MVP.css + Alpine.js for view routing
- BFF stores the wireframe at `.asdlc/wireframes/spec.html` in the project repo
- UI polls the status endpoint while generating; on completion, renders the HTML inside a sandboxed iframe
- User can regenerate after editing the spec — the previous spec is passed as delta context so the wireframe focuses on what changed
