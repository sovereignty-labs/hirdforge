# DESIGN.md — house design system

Brand contract for coding agents. Tokens plus the reasoning behind them, in the
[DESIGN.md](https://github.com/VoltAgent/awesome-design-md) tool-neutral shape: an
agent reads this file and generates UI that matches without being re-briefed.

**This file supersedes `docs/ui/DESIGN_SYSTEM.md` and `docs/ui/cockpit-mock.html`
for all colour.** Those still carry the retired copper-ember palette. Where they
and this file disagree on colour, **this file wins**; where they disagree on layout,
type, or component grammar, they are still correct and this file defers to them.
Read `docs/ui/spec.md` for surface-by-surface behaviour.

Palette locked 2026-07-25. Chosen head-to-head against a "Slate" alternative via
design-study artifacts.

---

## The law — read this before the tokens

**Cool phosphor is the machine. Warm ember-coral is the operator.**

- **Cool** (`--accent`, `--good`) = the system acting: alive, working, done, passed,
  evidence. Anything the machine owns.
- **Warm** (`--warn`) = **you**: needs-operator, held steps, the approval gate.
  Anything waiting on a human.

Ownership is legible from temperature alone, before any label is read. This is the
point of the palette, not decoration. **A surface that inverts it looks correct and
lies about who has to act** — which is worse than an ugly surface, because it
silently tells an operator they have nothing to do.

The ground carries a faint teal so it reads powered-on rather than dead grey. Green
here is not web-success green; it is an electric phosphor that glows. Copper is
**dropped** — if you find yourself writing `#B25E36` or `#D08453` you are working
from the superseded file.

---

## Tokens

Define on `:root` (light default). Override under **both**
`@media (prefers-color-scheme: dark)` **and** `:root[data-theme="dark"]` /
`:root[data-theme="light"]`, with the attribute selectors declared **after** the
media query so an explicit viewer toggle wins. Only ever redefine *tokens* in those
blocks — never restyle individual elements inside a media query.

### Dark — the home skin

```css
--ground:#0E1417;  --surface:#141D20;  --surface-2:#1B2629;
--line:#273A3B;    --line-soft:#1E2B2D;
--text:#E6F1EE;    --text-2:#98ADA8;   --text-3:#5E726D;
--accent:#31E0A3;  --accent-2:#5BEFBE; --accent-ink:#04231A;
--good:#31E0A3;    --good-bg:rgba(49,224,163,.13);
--warn:#FF8B5A;    --warn-bg:rgba(255,139,90,.15);
```

### Light — glow flattens to a calm deep-spring, no bloom

```css
--ground:#EDF4F1;  --surface:#FAFCFB;  --surface-2:#E1EDE9;
--line:#CFDFDA;    --line-soft:#DEEAE6;
--text:#0F201B;    --text-2:#495C57;   --text-3:#7B8F89;
--accent:#0E9E72;  --accent-2:#12B482; --accent-ink:#ffffff;
--good:#0E9E72;    --good-bg:rgba(14,158,114,.13);
--warn:#D06A34;    --warn-bg:rgba(208,106,52,.14);
```

**`crit` is not yet chosen.** Pick a red during build (roughly `#FF6B6B` on dark)
and record it here rather than inventing one per surface.

### Retired — hard fail if present

`#B25E36` `#D08453` (copper-ember accent) and the rest of that palette:
`#F1F1F4` `#141319` `#FBFBFC` `#1B1A21` `#EAEAEF` `#232128` `#DEDCE4` `#332F3B`
`#211F28` `#ECE8E3` `#57525F` `#A8A2B0` `#8B8694` `#736E7C` `#3E9E6D` `#63BC8C`
`#B98A22` `#E0B455` `#CC4E3B` `#E2664F`

---

## Glow — dark only, subtle

The machine is bioluminescent; it should not be a neon sign.

- Panel ambient: `radial-gradient(130% 90% at 50% -12%, rgba(49,224,163,.07), transparent 58%)`
- Bloom on: the Steward `◆` avatar (`0 0 16px -3px`), Dispatch button (`0 0 18px -5px`),
  live pulse dot, evidence chip (`0 0 12px -4px`), done badge, brand mark
- Log `PASS`: `text-shadow: 0 0 8px rgba(49,224,163,.45)`
- Plan-header icon: `drop-shadow(0 0 5px)`

Keep it a **forge at night, not fintech-mint.** The deep teal ground and the
ember-coral are what save it. If it runs hot, mute the phosphor toward sea-glass and
dim the bloom.

---

## Type

- UI/body: `system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`, base 13px.
  The native OS face is most of why an app feels native rather than like a web page.
- Data / logs / metrics: `ui-monospace, "SF Mono", "Cascadia Code", Menlo, Consolas, monospace`
- Scale: 10.5 (uppercase labels, `.08em` tracking) · 11 (meta) · 12.5–13 (UI) ·
  14 (emphasis) · 16 (section) · 19 (view title)
- `font-variant-numeric: tabular-nums` on **every** number that lines up in a column
- No webfonts. No external font requests.

## Layout & component grammar

- Full-viewport shell (`100dvh`). **The page never scrolls** — panels scroll
  internally; wide content gets its own `overflow-x: auto`.
- Panel radii 7–10px. Small details (dots, stripes, pills) take smaller radii —
  applying the panel figure to a 4px stripe is wrong.
- Spacing via flex/grid `gap`, never collapsing per-element margins.
- **Encode state in form, not only text:** a dot, pill, or stripe. `building` = warn
  pill, `done` = good pill, `waiting` = neutral. An approval gate carries a severity
  stripe — escalation is loud, never a footnote.
- **Every "done" shows its evidence** — a mono chip such as `secret Ready · 20s` or
  `ci green`. A green state you cannot back is forbidden.
- Motion subtle only (live-dot pulse, streaming caret, panel fade), guarded with
  `@media (prefers-reduced-motion: reduce)`. Always a visible `:focus-visible`.

---

## Conformance

Machine-checked by the `tasks_frontend` suite in the KWS catalog
(`agent-layer/benchmark/tasks_frontend.py`), which asserts against the exact token
values above: every hex must be in the locked set, the retired palette is a hard
fail, the operator/machine law must not be inverted, the `data-theme` override must
be declared after the media query, and the page must render dark with zero console
errors in headless chromium.

Run it with `python3 run.py <lane> tasks_frontend 3` from the benchmark harness.
If you change a token here, change it there too — the suite is the enforcement and
it will happily enforce a stale value.
