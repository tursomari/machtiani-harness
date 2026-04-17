# Issue: Keep user-directed authority context local without redefining `CurrentGoal()` globally

## Summary

The current authority-context work appears to be behaving well in practice and the relevant test suite is green. However, there is still a design concern worth capturing for follow-up: broadening `conversation.CurrentGoal()` to treat resumed `user_input` as the session's current goal may be too global a semantic change for what is fundamentally a localized user-directed-ask classification need.

This issue is **not** a request to change the current implementation immediately. It is a follow-up to evaluate whether the authority-context plumbing should remain as-is or be narrowed later after more real-world usage.

## Why this may be too broad

`CurrentGoal()` is used across general planner flows, not just user-directed ask detection. If resumed `user_input` becomes the canonical current goal everywhere, then a narrow answer to a suspended question can start steering broader planning/finalization behavior.

Illustrative example:

- broader goal: `Fix the flaky planner-menu-flow test without changing routing behavior.`
- suspended question: `Do you want the safer fix or the faster fix?`
- resumed user input: `Use the safer fix.`

If `CurrentGoal()` now resolves to `Use the safer fix.`, that may be exactly what the authority detector wants, but it may be too narrow for the rest of the planner, which still needs the full session goal.

Another example:

- broader goal: `Plan my Japan itinerary.`
- suspended question: `Kyoto or Tokyo first?`
- resumed user input: `Kyoto first.`

That reply is a decision *within* the goal, not obviously a replacement for the entire goal.

## Why this is still plausibly okay for now

- The authority-context path is currently working well enough to keep.
- The relevant tests are passing.
- Real sessions may show that treating resumed `user_input` as the effective goal is actually the simplest and most robust behavior.

So this is a design follow-up, not a rollback request.

## What to evaluate later

1. Confirm every meaningful caller of `CurrentGoal()` and decide whether each one really wants:
   - the broad session goal,
   - the latest goal update,
   - the latest resumed user authority input,
   - or some composition of those.
2. Review whether user-directed monitor/purifier logic should consume a more explicit "authority context" input instead of relying on a globally broadened `CurrentGoal()`.
3. Decide whether a separate helper would be clearer, for example something conceptually like:
   - `CurrentGoal()` for broad session intent
   - `CurrentAuthorityContext()` or similar for resumed user-owned decisions
4. Add regression tests that distinguish:
   - broad goal continuation,
   - resumed user-directed decisions,
   - and ordinary goal updates.

## Suggested acceptance criteria for a future follow-up

- [ ] Audit all `CurrentGoal()` call sites and document intended semantics.
- [ ] Add at least one regression test demonstrating whether resumed `user_input` should or should not replace the broader goal in non-authority planner flows.
- [ ] Decide explicitly whether the current global behavior is intentional API semantics or an expedient implementation detail.
- [ ] If needed, introduce a narrower helper and migrate only the authority-sensitive call sites.
