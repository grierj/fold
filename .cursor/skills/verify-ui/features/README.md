# Fold UI verification map

The pages and states `verify-ui` covers. Baseline: `verify-fold up`, `doctor` ok, `eval "$(verify-fold env RUN_ID)"`. `drive-ui.mjs` seeds on its first run.

- [Page checks](./page-checks.md) covers every main page at 390/768/1280 for overflow, clipped selects, targets, axe and screenshots.
- [Selected states](./selected-states.md) covers filter chips and card tags in their selected and unselected states, which must be visibly stronger when selected.
- [Time zone field](./time-zone-field.md) covers the shared time zone select on Schedule an event, the church-wide event form and the admin setting, which must not clip at 390.
