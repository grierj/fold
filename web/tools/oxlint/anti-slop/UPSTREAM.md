# Anti-slop upstream

Fold copied `skills/install-anti-slop/assets/anti-slop/` from
`d2a9aea3b896ffcb291a85f2c1301aa2543ad8c8`. Every file in the skill bundle matches
that commit byte for byte. [Upstream snapshot](https://github.com/dmmulroy/anti-slop/tree/d2a9aea3b896ffcb291a85f2c1301aa2543ad8c8/skills/install-anti-slop/assets/anti-slop)

The entry points are `web/tools/oxlint/anti-slop/index.ts` and
`web/tools/oxlint/anti-slop/effect/index.ts`. Fold enables the generic rules in
`web/.oxlintrc.json`. The Effect rules are opt-in. Fold has no direct Effect dependency.

Fold added this record and copied the upstream MIT license from the same commit.
The ESLint Stylistic code retains its own license and [provenance record](vendor/eslint-stylistic/UPSTREAM.md).
