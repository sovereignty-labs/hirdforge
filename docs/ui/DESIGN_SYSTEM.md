# Hirdforge v2 — Design System ("the quiet forge")

**The visual north-star is `docs/ui/cockpit-mock.html`** — open it in a browser (toggle
theme, click *Fleet & models*, open the gear). It supersedes `strawman.jsx` as the visual
reference. This file is the copy-pasteable spec behind it. Where this and the mock disagree,
the mock wins; where either and a frozen contract (`OBSERVABILITY_CONTRACT.md`) disagree,
the contract wins — flag it.

Hirdforge's UI has always felt "an engineer did the minimum." This system is the fix. It is
binding house style for **every** surface you touch from Phase 1 on — even a debug page
inherits it. Do not ship another default-browser screen.

## Why it feels like a real desktop app (the whole point)
1. **`system-ui` typeface.** The native OS face is *exactly* why owui / Claude Code / LM
   Studio / Local Studio feel native. Use `system-ui` for UI, `ui-monospace` for the Cortex
   log + metrics. This single choice does most of the "feels real" work.
2. **The familiar three-column shell** — threads · conversation · live inspector. Familiar
   on purpose; the craft is in the details below, not in novelty.
3. **One bold move, everything else quiet** — a single copper-ember accent; status colors
   kept separate from it.

## Tokens (both themes, token-level; never style inside a media query)
Define on `:root` (light default), override under `@media (prefers-color-scheme: dark)`
**and** `:root[data-theme="dark"]` / `[data-theme="light"]` so the viewer's toggle wins.

| token | light | dark | use |
|---|---|---|---|
| `--ground` | `#F1F1F4` | `#141319` | app background |
| `--surface` | `#FBFBFC` | `#1B1A21` | rails, panels |
| `--surface-2` | `#EAEAEF` | `#232128` | cards, inputs |
| `--line` | `#DEDCE4` | `#332F3B` | hairline borders |
| `--text` | `#211F28` | `#ECE8E3` | primary text |
| `--text-2` | `#57525F` | `#A8A2B0` | secondary |
| `--text-3` | `#8B8694` | `#736E7C` | meta/labels |
| `--accent` | `#B25E36` | `#D08453` | **the only bold color** — interactive + brand |
| `--good` | `#3E9E6D` | `#63BC8C` | status: passed/done |
| `--warn` | `#B98A22` | `#E0B455` | status: pending/attention |
| `--crit` | `#CC4E3B` | `#E2664F` | status: failed |

Grounds are **warm graphite** (iron, not pure grey); light theme is **cool neutral**, not
AI-cream. Accent is the forge ember — the identity. Status hues never double as the accent.

## Type
- UI/body: `system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`. Base 13px.
- Data/log/metrics: `ui-monospace, "SF Mono", "Cascadia Code", Menlo, Consolas, monospace`.
- Scale: 10.5 (uppercase labels, `.08em` tracking) · 11 (meta) · 12.5–13 (UI) · 14 (emphasis)
  · 16 (section) · 19 (view title). `tabular-nums` on **every** number that lines up.

## Layout & component grammar
- Full-viewport shell (`100dvh`), no page scroll — panels scroll internally; wide content
  gets its own `overflow-x:auto`.
- Radii 7–10px. Shadows soft and layered. Spacing via flex/grid `gap`, never per-element
  margins that collapse.
- **Encode state in form, not just text:** a `dot`/`pill`/`stripe`. `building`=warn pill,
  `done`=good pill, `waiting`=neutral. The Lockbox approval carries a **severity stripe** —
  escalation is loud, never a footnote (LOOP_SPEC move 7).
- **Every "done" shows its evidence** (a mono chip: `secret Ready · 20s`, `ci green`). This
  is D-GATE made visible — a green state you can't back is forbidden (see STEERING mandate).
- Motion: subtle only (live-dot pulse, streaming caret, panel fade); guard with
  `prefers-reduced-motion`. Visible `:focus-visible`.

## The five surfaces (map to LOOP_SPEC)
- **Cockpit** — conversation with the interlocutor + inline **plan card** (revise/dispatch);
  the "talk" that makes it feel like the apps Kit likes. (moves 1,3,4,7,8)
- **Live inspector** — active tasks, the deterministic **Cortex log**, evidence, the Lockbox
  gate. Observability *disclosed*, not dumped. (moves 2,5,6)
- **Fleet & models** — the "anvil" fold-in: per-lane GPU/VRAM/throughput + swap + per-seat
  assignment. Wire to the existing LiteLLM fabric, not a new controller. (property 1)
- **Memory (Seidr)** — skill-scoped recall surface.
- **Settings** — a true sectioned menu (General · Models & lanes · Connections · Fleet ·
  Appearance · Advanced), real controls. Not a config file.
