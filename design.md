# Design: Fold

The shared app system. Product decisions in `docs/plan/PLAN.md` remain authoritative.
This redesign keeps the chosen brand, routes and behavior; it changes composition and density.

## System
- Genre: playful, restrained app treatment. Tone: warm, practical, community-first.
- Shape: working app screens, not landing pages. No marketing hero or stock photography.
- Theme: Fold brand. Warm paper, self-hosted Inter, violet for actions and selections.
- Signature: a date-led agenda with clear RSVP choices and the groups behind those dates.
- Shell: existing desktop top navigation and five mobile tabs, church name and Fold mark.
- Audience and use: church members finding groups and attending; volunteers organizing.

## Tokens
- `web/src/index.css` @theme owns all color, font, radius and control tokens (DEC-60).
- Primary `--color-primary` is Fold Violet #7f47c7; hover #6938a7; text-on-primary white.
- Page #faf8f5; surface white; sunken #f5f5f4; text #1c1917; muted #57534e.
- Use semantic roles only; no palette utility classes, new font, CDN or dependency.
- `web/src/tokens.css` owns layout: 72rem outer cap, 48rem reading, 36rem forms.
- Gutters: 16px phone, 24px from 640px. Columns/sections: 32px; ordinary rows: 16px.
- Type: Inter throughout; body/inputs 16px, secondary 14px, sections 20px, titles 30px.
- Radius: 4px labels, 8px controls, 12px cards, 16px dialogs. Avatars/pills stay round.

## Composition
- `.fold-page`: outer page container; `.fold-page-narrow`: reading-width variant.
- `.fold-page-header`: heading/action row, divider below, wraps safely on phones.
- `.fold-section`: section divided by a top rule; prefer this to another nested card.
- `.fold-panel`: one independent surface; do not put another bordered card inside it.
- `.fold-list`: horizontal separators for a list. `.fold-list-row`: 16px vertical padding.
- `.fold-auth`: centred 36rem form container; `.fold-form`: one unboxed form column.
- `.fold-settings-grid`: sidebar/content from 1024px; sidebar/back-link visibility must use
  the same breakpoint. Below it, keep the selected section and a back link, not a full menu
  before the form. Do not hide the section's content.
- Home: greeting and compact upcoming dates, directory with its own search/filter row.
- Group: compact photo-led identity header retains the uniform scrim, inverse text/action
  and contrast contract; below it, tabs and sections use the shared page measure.
- Events: date, place and response form a coherent unit; dates read as an agenda, not tiles.
  Per-date editors stay one column even on desktop: viewport width is not their available width.
- Calendar: compact header and wrapping filters; keep the selected-day result close to the grid.
- Admin/settings: quiet navigation, visible section titles, horizontal rules, no card piles.

## Controls and states
- Keep existing `components/ui` APIs and their validation, disabled/loading/error semantics.
- Inputs and touch targets at least 44px. Focus is immediate, visible, never clipped.
- One primary action per view; selected RSVP and chips remain explicit in text and state.
- New copy has no uppercase/tracked labels, glued arrows, decorative dots or middle-dot strings.
  Existing test-contracted date punctuation remains; `.hallmark/verification.md` records the
  strict Hallmark exceptions. Do not silently change tests or claim a clean Floor.
- Preserve all content facts, handlers, ids, URLs, test hooks, consent and legal language.
- Keep all loading, empty, error, full and permission-dependent states. No invented states.
- No tests edited to accept the redesign. Record any copy tightened for a Hallmark gate.

## Motion and assets
- Still by default; state feedback only. No lift/scale on cards, no entrance animations.
- Existing covers, avatars, illustrations and mark remain; no invented people or image slots.
- Colors and font literals belong only to the existing theme block; SVG assets retain theirs.

## Exports
- `web/src/index.css`, `web/src/tokens.css`, `web/src/base.css`; existing Tailwind role names.
- Baseline and route inventory: `.hallmark/preflight.json`, `.hallmark/routes.md`.
- Verification, remaining flags and independent review: `.hallmark/verification.md`.
