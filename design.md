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
- Home: greeting and four compact upcoming dates above a full-width directory; two group
  columns from 1024px, never two squeezed into a side rail. Browse groups skips the agenda.
- Directory: sticky search/Filters/count toolbar, available tags disclosed on demand,
  selected tags removable inline. Start with 12 results and an explicit Show more button.
  Search, tags and expanded count live in the URL; return position is in memory under
  the user-keyed shell. Restore the originating row and focus when returning from detail.
- Group: compact photo-led identity header retains the uniform scrim, inverse text/action
  and contrast contract; below it, tabs and sections use the shared page measure.
- Events: date, place and response form a coherent unit; dates read as an agenda, not tiles.
  Per-date editors stay one column even on desktop: viewport width is not their available width.
- Long group/series schedules: 12 compact summaries per page with one expanded response
  area; short schedules retain direct answers. Keep mounted drafts across browsing.
- Calendar: 25-event agenda pages with date navigation and persistent filters. Page focus
  clears the measured toolbar. Phone Week selects one day; busy Month cells show counts.
- People/admin lists: search and truthful counts above 25-row pages; Add above results,
  secondary actions in menus. Preserve contact-request drafts and distinct permission rules.
- My groups: 12-row pages and URL search. Messages: 25-row cursor pages with subject/status
  filters and a real older-history path. Rooms: building groups and compact management rows.
- Admin/settings: quiet navigation, visible section titles, horizontal rules, no card piles.

## Controls and states
- Keep existing `components/ui` APIs and their validation, disabled/loading/error semantics.
- Inputs and touch targets at least 44px in both dimensions, including prose links and
  short text actions. Focus is immediate, visible, never clipped.
- Calendar Going rows use a quiet primary tint, solid accent and check-mark label, not a
  solid violet fill. Every agenda answer label sits below the time (PLAN DEC-71 / #228).
- Signed-out public dates: "Register as a guest" is the one primary CTA; "Log in to register"
  stays secondary.
- Ghosted / unanswered agenda rows use a quiet dashed border (not primary), so Going's primary
  accent stays the answered cue.
- Heading levels follow the host page: single-event and full Alerts sections start at h2;
  series dates sit under Upcoming dates, and the bell's sections under its own h2.
- Alert Archive uses an outlined 44px control with a prominent glyph, separate from its row link.
- One primary action per view; selected RSVP and chips remain explicit in text and state.
- New copy has no uppercase/tracked labels, glued arrows, decorative dots or middle-dot strings.
  Product-approved event/date wording remains (PLAN DEC-92). The user authorized fixing
  the three deferred copy findings; `.hallmark/verification.md` records the before/after wording.
- Preserve all content facts, handlers, ids, URLs, test hooks, consent and legal language.
- Keep all loading, empty, error, full and permission-dependent states. No invented states.
- Never weaken behavior assertions to accept styling. When the user authorizes a copy change,
  update exact wording expectations and retain interaction tests. Record the changed wording.

## Motion and assets
- Still by default; state feedback only. No lift/scale on cards, no entrance animations.
- Existing covers, avatars, illustrations and mark remain; no invented people or image slots.
- Group lists give only their first cover eager/high-priority loading; later covers stay lazy
  with async decoding. Keep explicit square dimensions and the user's focal point.
- Colors and font literals belong only to the existing theme block; SVG assets retain theirs.

## Exports
- `web/src/index.css`, `web/src/tokens.css`, `web/src/base.css`; existing Tailwind role names.
- Baseline and route inventory: `.hallmark/preflight.json`, `.hallmark/routes.md`.
- Verification, remaining flags and independent review: `.hallmark/verification.md`.
- Historical directory implementation and scale experiment: `.hallmark/busy-church.md`.
- Delivered roster/calendar/admin/message/room list patterns and proof: `.hallmark/busy-lists.md`.
- PR #225 review issues #226–#229 and CI follow-up: `.hallmark/pr225-followup.md`.
