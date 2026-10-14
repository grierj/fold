# Directory

The directory lists groups a visitor is allowed to see, opens one, and filters them by search text or tag.

## Sub-features

- `directory-open` opens a public group from its card.
- `directory-search` narrows the list to the query and shows an empty state when nothing matches.
- `directory-tags` filters by tag and clears back to the full list.
- `directory-signed-out` shows a public group to a visitor and hides a private group.

## How to get to it (user POV)

- Choose the `Fold` logo in the header. It goes to `/`.
- Open `/`.
- Open `/?tag=<tag>` from a shared link.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok`.
- An admin is signed in (the sign-in drive leaves `Verify Admin` signed in).
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/directory/`.

- **Create a public group to list.** Choose `Start a group`. Run `page.getByRole("link", { name: "Start a group" }).click()` from `/`, then `page.getByLabel("Group name").fill("Room 201 class")` and `page.getByRole("button", { name: "Create group" }).click()`. The heading `Group created` appears. Choose `Go to the group`. The level-1 heading reads `Room 201 class`.
- **Return to the directory.** Choose the `Fold` logo. Run `page.getByRole("link", { name: "Fold", exact: true }).click()`. The heading `Upcoming groups` is visible and a heading `Room 201 class` is visible.
- **Open the group.** Choose the `Room 201 class` heading. Run `page.getByRole("heading", { name: "Room 201 class" }).click()`. The URL matches `/groups/` followed by digits, and the level-1 heading reads `Room 201 class`.
- **Search.** Go back to `/`. Fill the search box. Run `page.getByPlaceholder("Search groups…").fill("Room 201")`. The `Room 201 class` heading stays visible. Replace the text with `zzzz-no-such`. The text `No groups found. Try a different search or start a new group.` appears and the `Room 201 class` heading is gone. Clear the box. The group heading returns.
- **Filter by tag.** Create a second public group named `Choir practice` with tags `choir` (the tags field placeholder is `e.g. young-adults, bible-study`; that label is not tied to the input, so fill `page.getByPlaceholder("e.g. young-adults, bible-study")`). On `/`, choose the `choir` chip. Run `page.getByRole("button", { name: "choir", exact: true }).first().click()` (each group card also has a `choir` tag button; the chip row comes first). The URL contains `tag=choir`, the chip has `aria-pressed="true"` and a checkmark, the line `Showing groups tagged choir. Clear` appears, `Choir practice` is visible, and `Room 201 class` is not. Choose `Clear`. The URL is `/` and both headings are visible.
- **Signed-out visitor.** Choose `Log out`. `Room 201 class` remains visible. A private group, if one was created with the `Private` radio, is not visible. `GET /api/v1/auth/me` from this browser returns 401.
- **Proof.** Screenshot the filtered directory and the opened group. `directory/list.png` shows the Fold header, the query or the selected tag, and the matching group name. `directory/group.png` shows the level-1 group heading. `directory/result.txt` names `directory-search` or `directory-open` and the entry point used.

## Gotchas

- The search placeholder uses the ellipsis character `…`, not three ASCII dots. A wait for a fixed time races the 300ms search debounce. Wait for the group heading or the empty-state text.
- Group cards repeat their tags as buttons with the same names as the chips. Scope a chip locator with `.first()` or check `aria-pressed`.
- Tag chips match case-insensitively. A link `/?tag=CHOIR` still selects the `choir` chip.
- Group cards use a level-3 heading inside a link. Clicking the heading opens the group. The group page then shows a level-1 heading of the same name.
- `Upcoming groups` is the directory section. `Coming up` is the event list and is absent when there are no events.
- An invite-only group shows an `Invite only` pill. A signed-out visitor does not see that group in the directory.
