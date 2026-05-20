# Scenario 2: Requirements Management

## Description
User creates and manages a multi-document requirements set that drives the development lifecycle. The main document is `requirements.md`; sibling docs (functional, non-functional, user stories) extend it and are typically generated from it via document-generation skills.

## User Scenarios

### 1. Bootstrap Main Requirements from a Prompt
- User navigates to project → Requirements tab
- User enters a natural-language description of what they want to build
- The BFF streams `requirements.md` from the agents-service `requirements-from-prompt` skill
- User reviews, edits if needed, and continues to other docs or saves

### 2. Add a Sibling Document via the "+" Menu
- User clicks the "+" button next to the search input in the explorer sidebar
- A menu opens listing supported document types: Functional Requirements, Non-Functional Requirements, User Stories
- User selects a type → a new file is created with the canonical filename (e.g. `functional-requirements.md`)
- If the type's source documents exist, the editor surfaces a "Generate from sources" CTA
- User clicks Generate → the BFF runs the type's skill against the source files and streams the result into the new doc

### 3. Edit Documents
- User edits any document in the markdown editor
- The active file's buffer is debounce-saved as a draft (`PUT /requirements/files/{name}`)
- Per-file dirty markers appear in the sidebar
- Reload-restore: drafts in localStorage survive pod restarts; on reload the page offers "use local / discard" when a draft diverges from the server

### 4. Save & Proceed (Tag the Bundle)
- User clicks "Publish" → BFF calls `POST /requirements/save`
- git-service stages the entire `specs/requirements/` directory, commits, pushes, and creates the next `v<N>` tag
- The tag covers all documents in one snapshot — adds, edits, deletes, renames are all bundled into one tag bump
- Status transitions from "draft" to "approved"; design generation becomes available

### 5. View Version History
- User opens the version selector → sees `v1, v2, v3, …`
- Selecting a version loads every document at that snapshot (read-only)
- The diff button compares the active file's working copy against its content at the latest tag

### 6. Discard Working-Copy Changes
- User has unsaved edits and clicks Discard
- BFF calls `POST /requirements/discard` → git-service reverts the working tree to the latest `v<N>` tag (file additions are removed; deletions are restored)

### 7. Delete / Rename a Sibling Document
- User opens the kebab menu on any sibling file → Rename or Delete
- `requirements.md` is protected and cannot be deleted or renamed
- Renames are committed atomically on the next save (`v<N+1>` tag captures the rename + any concurrent edits)
