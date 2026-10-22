# Household registration

An adult signs up named household people independently, sees each final answer, and can rename the household and edit its address in Profile. Managers see only authorized household details and time-limited care notes.

## Sub-features

- `household-profile`: rename and address edit/save/clear, including an empty household.
- `household-rsvp`: child-only and mixed household answers, independent modes and explicit childcare.
- `household-privacy`: pending links, unrelated count-only viewers, manager age without birth dates, and care windows.
- `household-roster`: registration-ID remove/promote/add-back and per-pool over-capacity confirmation.

## How to get to it (user POV)

- Profile → My household → Add a household member, Rename or Edit address.
- Group → its next date → Who will attend? → Save answers.
- Group → more dates → event → a date → Who will attend? → Save answers.
- Event → date's More menu → Manage sign-ups → Promote, Remove or Removed or left out → Add back.
- Admin → Approvals → Household links → Confirm, with a check note.

## Driving it with verify-fold

Preconditions: follow the verification map baseline, launch with `verify-fold up`, require `verify-fold doctor "$RUN_ID"` to print `ok`, and load the exported run environment. Use separate adult holder, unrelated member, event leader and admin accounts. Use a scheduled event starting within the next 24 hours for care-note proof, and another beyond that window. Use public visibility, independent in-person/remote limits and childcare.

- **Profile:** `await page.goto('/profile')`; click `getByRole('button', {name:'Rename'})`, fill `getByLabel(/^Household name/)` and `getByLabel(/^Address/)`, then click `getByRole('button', {name:'Save household'})`. Check success and focus return. Reload and check both saved values. Clear Address and save again. Repeat after removing all household people; the household name/address remain.
- **Create:** click `getByRole('button', {name:'Add a household member'})`. Add adult Mary with a birth date and child Kit with a birth date and long care notes. Before confirmation, the RSVP checkbox for Kit is disabled with “Waiting for an admin to confirm”. Confirm through the admin's Approvals page and reload as holder.
- **RSVP from both entry points:** open the group and then the event's date, select `getByRole('checkbox', {name:'Kit', exact:true})`, choose their mode and `getByRole('checkbox', {name:'Childcare for Kit'})`. Save with `getByRole('button', {name:'Save answers'})`. First prove child-only attendance, then independent Me/Mary/Kit answers. Force one waitlisted answer and check the result names every touched person without claiming all are going. Unselect a saved person and check their explicit decline. On a refused care request, fields and unnamed Ages remain.
- **Manager:** open the date's More menu, choose `getByRole('menuitem', {name:'Manage sign-ups'})`. Check `Kit · … household (via …)`, Child/Teen and Remote badges and integer age. Expand `getByRole('button', {name:'Show full care notes for Kit'})`; check `aria-expanded` and `aria-controls`, then collapse. Remove Kit and use Add back. Force the row's own mode full and accept the explicit over-capacity confirmation.
- **Privacy:** save raw event JSON via the browser request context for holder, manager, unrelated member and signed-out viewer. Unrelated responses must contain counts without household names, IDs, labels, ages or notes. Manager JSON has age without birth date/address; care is absent outside the window and for non-going answers. Keep the roster open across care expiry and focus the tab again; notes disappear and state refreshes.
- **Evidence:** at 390, 768 and 1280px, use `await page.setViewportSize({width,height:900})` and `await page.screenshot({path:process.env.VERIFY_EVIDENCE+'/household-registration/<state>-'+width+'.png',fullPage:true})`. Capture Profile edit/saved, selected RSVP/mixed result, manager roster and collapsed/expanded notes. Run the verify-ui measurements/axe, inspect every 390px image, and save a result.txt identifying each entry point and raw JSON assertions.

## Gotchas

- Never use the shared development or e2e server for interactive proof; preserve evidence after `verify-fold down`.
- The holder's unnamed childcare count excludes children selected by name. Remote switches explicitly clear named childcare in the draft.
- An existing answer's responsible holder does not transfer to another household adult. Disabled holders' answers are unavailable.
- A removed own answer must not hide the household editor. Seat-only manager removal keeps its existing promotion behavior in this slice.
- Do not tick browser or concurrency acceptance items without actual execution evidence.
