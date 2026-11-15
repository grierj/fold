# Directory

The directory lists groups a visitor is allowed to see, opens one, and filters them by search text or tag. It is the `Groups` section on the home page.

## Sub-features

- `directory-open` opens a public group from its card.
- `directory-search` narrows the list to the query and shows an empty state when nothing matches.
- `directory-tags` filters by tag from the `Filters` panel and clears back to the full list.
- `directory-signed-out` shows a public group to a visitor and hides a private group.

## How to get to it (user POV)

- Choose `Home` in the header's main navigation, or the church name at the left of the header. Both go to `/`. When signed in, `Browse groups` on the greeting jumps to the section.
- Open `/`.
- Open `/?tag=<tag>` from a shared link.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok`.
- An admin is signed in (the sign-in recipe leaves `Verify Admin` signed in).
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/directory/`.

- **Create a public group to list.** In the header's main navigation (`page.getByRole("navigation", { name: "Main" })`) choose `New group`, then `page.getByLabel("Group name").fill("Room 201 class")` and `page.getByRole("button", { name: "Create group" }).click()`. The level-1 heading `Group created` appears. Choose `Go to the group`. The level-1 heading reads `Room 201 class`.
- **Return to the directory.** Choose `Home` in the main navigation. The level-2 heading `Groups` is visible, the status line under the search box reads `N groups`, and a heading `Room 201 class` is visible.
- **Open the group.** Choose the `Room 201 class` heading. Run `page.getByRole("heading", { name: "Room 201 class" }).click()`. The URL matches `/groups/` followed by digits, and the level-1 heading reads `Room 201 class`.
- **Search.** Go back to `/`. Fill the search box. Run `page.getByLabel("Search groups").fill("Room 201")`. The `Room 201 class` heading stays visible. Replace the text with `zzzz-no-such`. The empty state `No groups match “zzzz-no-such”` with `Try a shorter word, or check the spelling.` appears and the `Room 201 class` heading is gone. Clear the box. The group heading returns.
- **Filter by tag.** Create a second public group named `Choir practice`. Its tags live under `More options`: run `page.getByRole("button", { name: /^More options/ }).click()` and `page.getByLabel("Tags").fill("choir")` before `Create group`. On `/`, choose `page.getByRole("button", { name: "Filters", exact: true })`, then the chip `page.getByRole("group", { name: "Group tags", exact: true }).getByRole("button", { name: "choir", exact: true })`. The URL contains `tag=choir`, the chip has `aria-pressed="true"` and a checkmark, a row `Any of:` with the chip `Remove choir filter` and a `Clear` button appears, `Choir practice` is visible, and `Room 201 class` is not. Choose `Clear` (`page.locator("#groups").getByRole("button", { name: "Clear", exact: true })`). The URL is `/` and both headings are visible. Opening `/?tag=CHOIR` shows `Filters (1)`, and the chip pressed in the panel is labelled `CHOIR`, as typed.
- **Signed-out visitor.** Create a group named `Members-only circle` with the `Private` radio (`page.getByRole("radio", { name: /^Private/ })`). The admin sees it on `/`. Choose `Log out`. `Room 201 class` remains visible and `Members-only circle` is not. `GET /api/v1/auth/me` from this browser returns 401.
- **Proof.** Screenshot the filtered directory and the opened group. `directory/list.png` shows the Fold header, the selected tag, and the matching group name. `directory/group.png` shows the level-1 group heading. `directory/result.txt` names `directory-search` or `directory-open` and the entry point used.

## Gotchas

- A wait for a fixed time races the 300ms search debounce. Wait for the group heading or the empty-state text.
- Group cards show their tags as plain text. The tag buttons are only the chips in the `Filters` panel, which is closed until `Filters` is chosen. The `Group tags` group holds `All` and one chip per tag in the church's groups.
- The `aria-pressed` attribute follows the URL one render later. Wait for it (`getByRole("button", { name: "choir", pressed: true })`) instead of reading it right after `waitForURL`.
- Tag chips match case-insensitively. A link `/?tag=CHOIR` still selects the group's tag, and the chip text is the spelling in the link.
- Group cards use a level-3 heading inside a link. Clicking the heading opens the group. The group page then shows a level-1 heading of the same name.
- The signed-out home page also has a `Church events` list when church-wide events exist. The signed-in home page has `Coming up for you` instead.
- An invite-only group shows an `Invite only` pill. A signed-out visitor does not see that group in the directory, and a private group shows `Private` to the people who can see it.
- With no groups at all the section says `No groups yet`, with `Start a group` for a signed-in person and `Log in` for a visitor.
