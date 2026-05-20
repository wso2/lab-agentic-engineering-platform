# Writing Requirements in EARS Format

A concise prompt/reference for generating clear, testable requirements using **EARS** (Easy Approach to Requirements Syntax). EARS constrains natural-language requirements into a small set of patterns that eliminate ambiguity, missing triggers, and vagueness.

---

## 1. Universal Template

```
While <preconditions>, when <trigger>, the <system> shall <response>.
```

Cardinality rules:
- **Preconditions** (`While …`): 0 or more
- **Optional-feature qualifiers** (`Where …`): 0 or more — *separate from preconditions*
- **Trigger** (`When …`): 0 or 1 (event trigger). `If … then` is a separate pattern reserved for unwanted behaviour (§2.5) — not a variant of `When`.
- **System name**: exactly 1
- **System response** (`shall …`): 1 per requirement — split on `and`

Only the universal form `While … when … shall …` has a canonical clause order. When combining other keywords, choose the order that reads clearly.

---

## 2. The Five Patterns

Pick the pattern by asking: *"Under what circumstances must the system do this?"*

### 2.1 Ubiquitous — always active (no keyword)
For invariants: constraints, global properties, most non-functional requirements.

> `The <system> shall <response>.`

`The payment service shall encrypt all card numbers at rest using AES-256.`

### 2.2 State-Driven — `While`
Behaviour applies **for the duration** a state holds.

> `While <state>, the <system> shall <response>.`

`While a user is authenticated, the console shall display the user's project list in the sidebar.`

### 2.3 Event-Driven — `When`
Behaviour is triggered **at the moment** an event occurs.

> `When <trigger>, the <system> shall <response>.`

`When a user clicks "Start Implementation", the BFF shall create one ComponentTask per component in the plan.`

**Disambiguator:** `When` = moment in time (an event fires). `While` = duration (a state persists). *"When logging in"* is wrong — it's really *"When the user submits login credentials"*.

### 2.4 Optional Feature — `Where`
Behaviour applies **only if** an optional feature/configuration is present.

> `Where <feature is included>, the <system> shall <response>.`

`Where GitHub PAT authentication is configured, the git-service shall sign commits with the PAT identity.`

### 2.5 Unwanted Behaviour — `If … then`
Response to errors, invalid input, or undesired situations.

> `If <unwanted condition>, then the <system> shall <response>.`

`If the Claude CLI process exits with a non-zero code, then the remote-worker shall mark the ComponentTask as "failed" and persist the exit code.`

---

## 3. Complex Requirements

Combine keywords when a response genuinely depends on multiple conditions. The canonical combination is `While … when … shall …`:

`While the component is in "implementing" status, when the agent calls submit_implementation, the BFF shall transition the component to "committing" and invoke git-service.`

Other combinations (e.g. `While … if … then …`) are also legal in EARS — use them only when clearer than splitting into two requirements.

*Style guideline (not part of EARS):* keep one requirement to a single sentence with ≤ 3 preconditions. Beyond that, split or use a table.

---

## 4. Writing Rules

**Structure**
- Use `shall` for required behaviour; never `should`/`will`/`must`.
- One `shall` per requirement — split compound actions joined by `and`.
- Name a single, concrete system (`the BFF`, `the git-service`), not "the platform".
- Use active voice: *"the system shall process"* — not *"data will be processed"*.

**Clarity & testability**
- The response must be **observable and verifiable** — include measurable criteria (time, count, unit, enum, exact message).
- If the input gives no numeric target, write `TBD` rather than inventing one (e.g. `within TBD seconds`).
- No vague qualifiers: `appropriate`, `reasonable`, `user-friendly`, `fast`, `robust`, `efficient`, `etc.`
- No hidden behaviour. Every trigger and precondition must be objectively detectable.

**Pattern discipline**
- Don't confuse state and event: a gerund like "while logging in" is really an event (`When the user submits credentials`).
- Don't conflate goals with requirements. "Delight users" is a goal; it is not deliverable by one system.
- Non-functional requirements (performance, security, availability) are usually **Ubiquitous** with a measurable bound.
- For every happy-path `When`, ask *"what can fail?"* and add a paired `If … then` for each applicable failure mode: **(a) invalid input, (b) timeout / no response, (c) downstream dependency failure, (d) permission/auth failure**.

---

## 5. Procedure — Generating Requirements From a Description

1. **List assumptions first.** If the input lacks a system name, trigger, or measurable response, state the assumptions you're making at the top of the output before any requirements. Do not silently fill gaps.
2. **Identify the system(s).** If no component is named, infer the owning component from the feature domain (e.g. "auth" → the auth service). If you cannot infer it, flag it as an assumption.
3. **Enumerate behaviours.** For each user action, external interface, and scheduled/background job, write one requirement.
4. **Pick the pattern** using the cheatsheet below.
5. **Write the response concretely.** What is observable after it completes? Include units, limits, error messages, and return codes. Use `TBD` for unknown numerics.
6. **Pair each event-driven requirement with its unwanted-behaviour counterparts** (invalid input, timeout, downstream failure, auth failure) — this is where most requirement sets are weak.
7. **Add ubiquitous NFRs** — performance, security, privacy, availability — each with a measurable bound.
8. **Validate each requirement** against the checklist:
   - [ ] Matches one pattern (or a documented combination).
   - [ ] One concrete system, one `shall`, one sentence.
   - [ ] Trigger/state is detectable; response is verifiable.
   - [ ] No vague adjectives, no invented numerics, no `and`-joined actions.

**Pattern cheatsheet**

| Situation | Pattern | Keyword |
|---|---|---|
| Always true (invariant, NFR) | Ubiquitous | — |
| True while a state persists | State-Driven | `While` |
| Triggered at the moment of an event | Event-Driven | `When` |
| Applies only if a feature is present | Optional | `Where` |
| Response to an error / invalid input | Unwanted Behaviour | `If … then` |
| Needs both a state and a trigger | Complex | `While … when …` |

---

## 6. Output Format

Emit a Markdown document. Group requirements under `##` headings by feature area. Each requirement is one bullet:

```
- **REQ-<AREA>-<NNN>** `[<Pattern>]` — <EARS sentence>
```

- `REQ-<AREA>-<NNN>` — stable ID; `AREA` is an uppercase short tag per heading (e.g. `AUTH`, `IMPL`); `NNN` is zero-padded, unique within the file.
- `[<Pattern>]` — one of: `Ubiquitous`, `State-Driven`, `Event-Driven`, `Optional`, `Unwanted`, `Complex`.
- **Rationale** — add a nested sub-bullet `_Why:_ …` only when the trigger or response is non-obvious.

If assumptions were made, list them under a `## Assumptions` section at the top.

**Example output**

```markdown
## Assumptions
- The "console" refers to the React frontend at port 8090.
- Build timeouts are not specified in the input; marked TBD below.

## Implementation pipeline

- **REQ-IMPL-001** `[Event-Driven]` — When the user clicks "Start Implementation", the BFF shall create one ComponentTask record per component in the plan.
- **REQ-IMPL-002** `[Event-Driven]` — When a ComponentTask is created, the BFF shall dispatch it to the remote-worker via POST /dispatch.
- **REQ-IMPL-003** `[Unwanted]` — If the remote-worker returns a non-2xx response, then the BFF shall mark the ComponentTask as "failed" and record the response body.
- **REQ-IMPL-004** `[Unwanted]` — If the remote-worker does not respond within TBD seconds, then the BFF shall mark the ComponentTask as "failed" with reason "dispatch_timeout".
- **REQ-IMPL-005** `[Ubiquitous]` — The BFF shall persist all ComponentTask state transitions to PostgreSQL before returning a response.
- **REQ-IMPL-006** `[Complex]` — While a ComponentTask is in "implementing" status, when the agent calls submit_implementation, the BFF shall transition the task to "committing" and invoke git-service.
```

---

## Appendix — When NOT to Use EARS

EARS is a prose format. Prefer another notation when it would obscure rather than clarify:
- **Mathematical formulas or algorithms** — write the formula.
- **State machines with many transitions** — use a state diagram or transition table.
- **Decision logic with many conditions** — use a decision table.
- **More than 3 preconditions in one requirement** — split, or use a list/table inside the statement. *(Style guideline, not an EARS rule — see §3.)*
- **Goals and user stories** — keep in their own section; EARS supplements intent, it does not replace it.
