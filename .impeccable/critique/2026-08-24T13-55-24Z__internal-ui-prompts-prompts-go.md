---
target: box add interactive flow / prompts.go
total_score: 20
max_score: 40
na_heuristics: 
p0_count: 1
p1_count: 2
timestamp: 2026-08-24T13-55-24Z
slug: internal-ui-prompts-prompts-go
---
#### Design Health Score

| # | Heuristic | Score | Key Issue |
|---|-----------|-------|-----------|
| 1 | Visibility of System Status | 2 | Choices reprint as scroll history; no stable status region or step N/M |
| 2 | Match System / Real World | 3 | Domain terms mostly fit; Acquisition/credential jargon still tax first-timers |
| 3 | User Control and Freedom | 2 | Abort exists; prior choices visible but not editable without restart |
| 4 | Consistency and Standards | 1 | Four languages: branded intro, themed Huh, unstyled ASCII table, indented Review |
| 5 | Error Prevention | 2 | Validations + confirm help; stacked DO fields and late price raise misconfig risk |
| 6 | Recognition Rather Than Recall | 2 | Table aims at recognition but forces full re-scan after every step |
| 7 | Flexibility and Efficiency | 3 | Flags/--yes skip well; interactive path is heavy and non-revisitable |
| 8 | Aesthetic and Minimalist Design | 1 | Full-table reprints are anti-minimal; hierarchy collapses to monochrome |
| 9 | Error Recovery | 2 | Abort/cancel exist; mid-wizard “change that choice” does not |
| 10 | Help and Documentation | 2 | Some field descriptions; bare sections lack wizard context |
| **Total** | | **20/40** | **Acceptable — significant improvements needed** |

#### Design Specificity Verdict

**LLM assessment**: After a brief teal “schooner” + wave beat, the Operate wizard becomes interchangeable admin-form CLI. Plain section titles, Huh fields, and a growing “Choices so far” ledger could belong to any provisioning tool. Brand lives in the curtain, not in the work.

**Deterministic scan**: `detect.mjs` on `internal/ui/prompts/prompts.go` (and nearby UI packages) exited 0 with `[]` findings — inapplicable to Go CLI printers, not affirmative clean UI. Manual evidence: screenshot shows **3×** “Choices so far”, **3** full cumulative table reprints (1→3→4 rows), **3** unstyled section titles, brand color only on intro; source confirms `section()` is plain `fmt` with **0** theme Style uses, and Review screens use a separate indented dump format.

**Visual overlays**: Not available — terminal scrollback / static PNG, not a DOM target.

#### Overall Impression

The intro promises craft; the wizard delivers paperwork. The single biggest opportunity is to stop treating the TTY as an append-only audit log and give the flow one stable chrome language (status + step + themed section) that stays present after the logo fades.

#### What's Working

- Intro animation + Primary teal is a real brand moment and the only composed hierarchy in the flow.
- Acquisition fork and flag-driven skip paths respect CLI-first users who already know the shape.
- `ConfirmProvision` surfaces monthly estimate and billable stakes before create — correct for Operate.

#### Priority Issues

- **[P0] Scroll-append full “Choices so far” table**
  - **Why it matters**: Burns scroll, repeats settled facts, competes with the next prompt; feels like a chat log of forms, not a wizard.
  - **Fix**: One stable summary region (overwrite/in-place) or a compact sticky status line; stop reprinting the entire ledger after every `RecordChoices`.
  - **Suggested command**: `$impeccable distill` then `$impeccable layout`

- **[P1] Visual-language fracture**
  - **Why it matters**: Intro → plain titles → ASCII table → Review dump reads as four products stitched together; confidence drops at the billable moment.
  - **Fix**: One chrome system for section, summary, and review; route non-Huh output through `theme.Style` (Primary section, Muted labels, Success on settled values).
  - **Suggested command**: `$impeccable typeset` / `$impeccable colorize`

- **[P1] Wizard without orientation or revision**
  - **Why it matters**: No step N/M; choices visible but not editable; restart is the only escape hatch for a wrong earlier answer.
  - **Fix**: Step indicator + back/edit previous answers (or at least an “edit summary” before create).
  - **Suggested command**: `$impeccable shape` (interaction model) then `$impeccable onboard`

- **[P2] Theme roles unused outside Huh/intro**
  - **Why it matters**: Palette exists (`#00B8A6` Primary, Muted, Success) but summary/sections ignore it — product character evaporates after frame one.
  - **Fix**: Apply muted labels, primary section titles, success on committed values in the summary chrome.
  - **Suggested command**: `$impeccable colorize`

- **[P2] Droplet configuration wall**
  - **Why it matters**: Size/image/VPC/keys/backups/IPv6 land in one group; catalogs exceed working-memory limits with no recommended default callout.
  - **Fix**: Progressive disclosure + highlight recommended defaults.
  - **Suggested command**: `$impeccable distill`

#### Persona Red Flags

**Alex (Power User)**: Forced re-reading of growing tables; cannot patch one prior field without abort/restart; will flee to flags and never return to the TUI.

**Jordan (First-Timer)**: Friendly intro then cold titles (“Acquisition”, `dop_v1_`) with no step count; Review format switch at billing looks like a different app — high abandon risk at Create.

**Terminal-native developer**: Expects stable viewport status and muted chrome, not cargo-cult box-drawing that grows; unstyled sections and printf Review at peak stakes feel unfinished.

#### Minor Observations

- `section()` is literally `"\n%s\n"` — no bold, color, or step prefix.
- Summary booleans render as `%t` (`true`/`false`) — machine voice in a human summary.
- Screen clear at start then pure append makes late steps feel like scrolling a chat log.
- Accessible mode skips animation (good) but inherits the same monochrome dump pattern.
- Cognitive load: **7/8 checklist failures** (high); decision points with >4 options include Region, Size, Image, SSH keys.

#### Questions to Consider

- If the choice summary vanished, would users feel less oriented — or less annoyed?
- Why is the billable Review the least designed frame in the wizard?
- Should “Choices so far” be a status line you trust, or evidence you’re being audited?
- What would this flow look like if teal never left the viewport after the intro?
- Is the ASCII table solving recognition, or performing “CLI polish” for screenshot culture?
