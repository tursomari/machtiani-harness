# Final answer presentation proposal

This is a visual design proposal, not an implementation specification. Preview
it with the repository's actual semantic palettes:

```bash
./scripts/final-answer-presentation-demo.sh --theme terminal
./scripts/final-answer-presentation-demo.sh --theme machtiani-dark
./scripts/final-answer-presentation-demo.sh --theme machtiani-light
./scripts/final-answer-presentation-demo.sh --theme none
./scripts/final-answer-presentation-demo.sh --glyphs ascii
./scripts/final-answer-presentation-demo.sh --outcome interrupted
./scripts/final-answer-presentation-demo.sh --outcome input
```

The `terminal` profile deliberately uses the terminal's own ANSI colors. The
dark and light profiles use Machtiani's optional true-color palettes. Glyphs are
configured independently with `[ui].glyphs = "unicode"` or `"ascii"`; the
`MACHTIANI_GLYPHS` environment variable provides a temporary override.

## Recommended composition

```text
  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Here are the last 5 commit messages:

  1.  bdd1c6804  feat(session): show final answer path before continuation
  2.  f49d1345f  feat(ui): reorganize footer token metrics.
  3.  15246b177  feat(ui): show active context window.
  4.  443cf9bdc  feat(ui): add active context and cwd to footer
  5.  6901c1892  test(discovery): harden native file operations


  Answer saved to:
    ~/.machtiani/7e6be546-9043-42d6-90c9-13cf67c2421f/sessions/agent-20260718T051605-0813/chat/agent-final-answer.md

  Resume this session:
    ──────────────────────────────────────────────────────────────────────────────────────────────
    $ mct-agent run -t "<your follow-up prompt>" --session agent-20260718T051605-0813
    ──────────────────────────────────────────────────────────────────────────────────────────────

  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  21s  ~/projects/mct  session token input 24,706 (cache 88%)  output 1,152
  code-forge-skyvern turn 1 running  session agent-20260718T051605-0813  planner deepseek:deepseek-v4-flash high  shell deepseek:deepseek-v4-flash high
```

## Semantic treatment

- The heavy outer rules use **Beauty**. They replace the protocol-like `FINAL
  RESPONSE` banner and frame the answer, saved artifact, and next action as one
  user-facing conclusion.
- `Answer saved to:` uses **Provenance**. The path uses underlined **Beauty**,
  matching the existing link treatment while remaining easy to copy.
- `Resume this session:` and the command rules use **Goodness**. The bounded
region makes it obvious that the command is intended to be copied, edited,
and run. Its rules are sized to the longest command row plus two columns of
right padding.
- Inside the command, `$` uses **Goodness**, `mct-agent run` uses
  **Provenance**, and the editable arguments remain the terminal foreground.
- The footer keeps its current role-based styling: elapsed time, turn, and
  running state use **Truth**; the mode uses **Beauty**; session/model identity
  and numeric evidence use **Provenance**.

The outer frame is intentionally the only full-width separator. Adding another
full-width rule between the answer and `Answer saved to:` would split the result
back into competing panels. Spacing and semantic color are enough there. The
command retains its own lighter rules because it is an interactive copy/edit
surface rather than ordinary prose. A command stays on one line with a leading
`$` when it fits. At narrower widths it becomes canonical multiline Bash with
continuation backslashes and no prompt marker.

The same renderer owns all conclusion outcomes. A resumable interruption uses
the red `SHELL-AGENT INTERRUPTED` heading and a resume command without `-t`. A
user-input suspension uses an amber `USER INPUT NEEDED` heading, optional
`Why this needs your input:` context, a Beauty-styled decision, and
`<your answer>` in the resume command. Neither outcome invents a final
answer or saved-answer path.

## Palette reference

| Role | Terminal profile | Machtiani dark | Machtiani light |
|---|---|---|---|
| Truth | ANSI cyan | `#58C7D9` | `#006B78` |
| Goodness | ANSI green | `#74C991` | `#226B3A` |
| Beauty | ANSI magenta | `#C39BE8` | `#70428F` |
| Provenance | ANSI yellow | `#D7B45A` | `#795A00` |
| Rupture | ANSI red | `#E06C75` | `#A72E3F` |

## Glyph modes

- `unicode` (default): heavy `━` outer frame and light `─` command rules.
- `ascii`: `=` outer frame and `-` command rules for terminals or fonts with
  weak box-drawing support.

The outer frame prefers 96 glyphs, shrinks to stay one column inside the
terminal's auto-wrap edge, and disappears at extremely small widths. The footer
continues to use its existing responsive degradation independently.

Underscores are not recommended. They sit on the text baseline, resemble a form
blank, and are visually uneven across fonts. Box-drawing rules communicate
structure more directly; the ASCII variant remains a safe fallback.
