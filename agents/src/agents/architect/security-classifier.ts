// Pure rubric helper that mirrors the `exposesAPI` classification rules
// in prompt.ts ("API security classification (`exposesAPI`)"). Kept as
// a standalone function so the rubric is:
//   1. Unit-testable in CI without an LLM round-trip.
//   2. Available as a deterministic fallback (e.g. for non-LLM design
//      paths the BFF emits).
//
// The architect agent is told the same rule in natural language; this
// helper is the executable mirror.
//
// Returns:
//   - 'required' when the description triggers any of the protected
//     keyword families. Callers should map this to
//     `exposesAPI: { auth: end-user-required, userContext: X-User-Id }`.
//   - 'none' when nothing triggers (the canonical "public" hint).
//     Callers should OMIT the `exposesAPI` block entirely (the BFF
//     reads absence as public — see services.ResolveAPISecurityEnabled).

export type APISecurityHint = "required" | "none";

interface KeywordRule {
  family: string;
  patterns: RegExp[];
}

// The keyword catalog from prompt.ts. Each family corresponds to one
// "default required when …" bullet in the rubric. RegExp word boundaries
// keep accidental substring matches (e.g. "billing" inside "rebilling")
// out of the way; case-insensitive across the board.
const PROTECTED_RULES: KeywordRule[] = [
  {
    family: "auth-verbs",
    patterns: [
      /\blogin\b/i,
      /\bsign[- ]?in\b/i,
      /\bauthenticate(d|s)?\b/i,
      /\bauthentication\b/i,
      /\bsession(s)?\b/i,
    ],
  },
  {
    family: "identity-tokens",
    patterns: [
      /\bOAuth\b/i,
      /\bOIDC\b/i,
      /\bJWT\b/i,
      /\bbearer\s+token\b/i,
      /\bAPI\s+key(s)?\b/i,
    ],
  },
  {
    family: "access-intent",
    patterns: [
      /\bprotected\b/i,
      /\bprivate\b/i,
      /\binternal[- ]only\b/i,
      /\bauthori[sz]ed\b/i,
      /\bpermission(s)?\b/i,
      /\brole(s)?\b/i,
      /\bscope(s)?\b/i,
    ],
  },
  {
    family: "user-scoped-data",
    patterns: [
      /\bcustomer(s)?\b/i,
      /\btenant(s)?\b/i,
      /\buser\s+account(s)?\b/i,
      /\buser\s+data\b/i,
      /\buser\s+profile(s)?\b/i,
      /\bpersonal\b/i,
      /\bPII\b/,
    ],
  },
  {
    family: "payment-regulated",
    patterns: [
      /\bbilling\b/i,
      /\bpayment(s)?\b/i,
      /\bsubscription(s)?\b/i,
      /\binvoice(s)?\b/i,
      /\bcredit\s+card\b/i,
      /\bPCI\b/,
      /\bHIPAA\b/,
      /\bGDPR[- ]restricted\b/i,
    ],
  },
];

/**
 * Classify a free-form description against the `exposesAPI` rubric.
 *
 * @param text Combined description: typically the component's
 *   `componentAgentInstructions` plus, when available, the relevant
 *   slice of the project spec.
 * @returns 'required' or 'none'. Callers translate 'none' into "omit
 *   the api block" — never write `security: none` to disk.
 */
export function classifyAPISecurity(text: string): APISecurityHint {
  if (!text) return "none";
  for (const rule of PROTECTED_RULES) {
    for (const pattern of rule.patterns) {
      if (pattern.test(text)) {
        return "required";
      }
    }
  }
  return "none";
}

/**
 * matchingFamilies — debug helper that reports which keyword families
 * triggered. Useful for golden tests when you want to assert WHY a
 * description classified as 'required'.
 */
export function matchingFamilies(text: string): string[] {
  if (!text) return [];
  const hits: string[] = [];
  for (const rule of PROTECTED_RULES) {
    for (const pattern of rule.patterns) {
      if (pattern.test(text)) {
        hits.push(rule.family);
        break;
      }
    }
  }
  return hits;
}
