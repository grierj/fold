import AxeBuilder from "@axe-core/playwright";
import { expect, test, type APIRequestContext, type Page, type Request, type Route } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { apiAs, signInTo } from "./helpers";
import { formatCalendarDay } from "../src/lib/when";
// @ts-expect-error plain JS measurement shared with verify-ui
import { measure } from "./ui-measure.mjs";

const day = (n: number) => new Date(Date.now() + n * 864e5).toISOString().slice(0, 10);
const proof = process.env.SCREENS_DIR;
const evidence: unknown[] = [];

async function createEvent(request: APIRequestContext, title: string, days: number, recurrenceRule: string, extra: Record<string, unknown> = {}) {
  const admin = await apiAs(request, "grier");
  const { event } = await admin("POST", "/events", {
    title, startsLocal: `${day(days)}T18:00`, durationMinutes: 60,
    tzid: "UTC", location: "Hall", recurrenceRule, visibility: "public", ...extra,
  }) as { event: { id: number } };
  return event;
}

async function eventDates(request: APIRequestContext, eventId: number) {
  const admin = await apiAs(request, "grier");
  const detail = await admin("GET", `/events/${eventId}`) as {
    occurrences: { date: string; attendees: { userId: number; registrationId: number }[] }[];
  };
  return detail.occurrences;
}

async function answerDate(request: APIRequestContext, user: string, eventId: number, date: string, status: "going" | "maybe" | "declined") {
  const actor = await apiAs(request, user);
  await actor("PUT", `/events/${eventId}/registrations`, { date, status });
}

function answerGroups(page: Page) {
  return page.getByRole("group", { name: "Your answer" });
}

async function holdNextPostResponse(page: Page, path: string) {
  const pattern = `**${path}`;
  let release!: () => void;
  let committed!: () => void;
  let finish!: () => void;
  const released = new Promise<void>((resolve) => { release = resolve; });
  const mutationCommitted = new Promise<void>((resolve) => { committed = resolve; });
  const finished = new Promise<void>((resolve) => { finish = resolve; });
  let held = false;
  const handler = async (route: Route) => {
    if (route.request().method() !== "POST" || held) return route.continue();
    held = true;
    const response = await route.fetch();
    committed();
    await released;
    await route.fulfill({ response });
    finish();
  };
  await page.route(pattern, handler);
  return {
    committed: mutationCommitted,
    release: async () => {
      release();
      await finished;
      await page.unroute(pattern, handler);
    },
  };
}

async function holdNextEventRefresh(page: Page, eventId: number) {
  const pattern = `**/api/v1/events/${eventId}`;
  let release!: () => void;
  let started!: () => void;
  let finish!: () => void;
  const released = new Promise<void>((resolve) => { release = resolve; });
  const refreshStarted = new Promise<void>((resolve) => { started = resolve; });
  const finished = new Promise<void>((resolve) => { finish = resolve; });
  let held = false;
  const handler = async (route: Route) => {
    if (route.request().method() !== "GET" || held) return route.continue();
    held = true;
    const response = await route.fetch();
    started();
    await released;
    await route.fulfill({ response });
    finish();
  };
  await page.route(pattern, handler);
  return {
    started: refreshStarted,
    release: async () => {
      release();
      await finished;
      await page.unroute(pattern, handler);
    },
  };
}

async function capture(page: Page, name: string, fullPage = true, widths = [390, 768, 1280]) {
  for (const width of widths) {
    await page.setViewportSize({ width, height: 900 });
    const layout = await page.evaluate(measure);
    const { violations } = await new AxeBuilder({ page })
      .include("main")
      .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"])
      .analyze();
    const axe = violations.filter((item) => item.impact === "serious" || item.impact === "critical").map((item) => ({
      id: item.id,
      targets: item.nodes.map((node) => node.target.join(" ")),
    }));
    const actionTargets = await page.getByRole("group", { name: "Your answer" }).evaluateAll((groups) => groups.flatMap((group) =>
      Array.from(group.querySelectorAll("button")).filter((button) => button.getClientRects().length > 0).map((button) => ({
        name: button.textContent?.trim() || "button",
        width: Math.round(button.getBoundingClientRect().width),
        height: Math.round(button.getBoundingClientRect().height),
      })),
    ));
    const dialogTargets = await page.getByRole("dialog").evaluateAll((dialogs) => dialogs.flatMap((dialog) =>
      Array.from(dialog.querySelectorAll("button")).filter((button) => button.getClientRects().length > 0).map((button) => ({
        name: button.textContent?.trim() || "button",
        width: Math.round(button.getBoundingClientRect().width),
        height: Math.round(button.getBoundingClientRect().height),
      })),
    ));
    expect(layout.overflow, `${name}@${width} overflow`).toBe(0);
    expect(layout.cutOff, `${name}@${width} cut off`).toEqual([]);
    expect(layout.clipped, `${name}@${width} clipped`).toEqual([]);
    expect(layout.under24, `${name}@${width} 24px targets`).toEqual([]);
    expect(axe, `${name}@${width} axe`).toEqual([]);
    if (width === 390) {
      expect([...actionTargets, ...dialogTargets].filter((target) => target.width < 44 || target.height < 44), `${name}@390 RSVP targets`).toEqual([]);
    }
    evidence.push({ name, width, layout, axe, actionTargets, dialogTargets });
    if (proof) {
      mkdirSync(proof, { recursive: true });
      await page.screenshot({ path: `${proof}/${name}-${width}.png`, fullPage, animations: "disabled", caret: "hide" });
      writeFileSync(`${proof}/results.json`, JSON.stringify(evidence, null, 2));
    }
  }
  await page.setViewportSize({ width: 390, height: 900 });
}

test("all three series scopes submit, keyboard focus is contained, bulk Can't go is confirmed, and a single date bypasses the dialog", async ({ page, request }) => {
  test.setTimeout(180_000);
  const event = await createEvent(request, "Series scope browser proof", 5, "FREQ=WEEKLY;COUNT=4");

  await page.setViewportSize({ width: 390, height: 900 });
  await signInTo(page, "sam", `/events/${event.id}`);
  await expect(answerGroups(page)).toHaveCount(4);

  const firstGoing = answerGroups(page).first().getByRole("button", { name: "Going" });
  await firstGoing.click();
  let dialog = page.getByRole("dialog", { name: /How far should “Going” apply/ });
  const firstOption = dialog.getByRole("button", { name: "Just this date" });
  await expect(firstOption).toBeFocused();
  const phoneDialog = await dialog.boundingBox();
  expect(phoneDialog && Math.abs(phoneDialog.y + phoneDialog.height - 900)).toBeLessThanOrEqual(1);
  await capture(page, "series-scope-dialog", false);
  await page.keyboard.press("Shift+Tab");
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(firstOption).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(firstGoing).toBeFocused();

  await answerGroups(page).first().getByRole("button", { name: "Maybe" }).click();
  dialog = page.getByRole("dialog", { name: /How far should “Maybe” apply/ });
  await expect(dialog.getByRole("button", { name: "Just this date" })).toBeFocused();
  await dialog.getByRole("button", { name: "Just this date" }).click();
  await expect(answerGroups(page).first().getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "true");
  for (let index = 1; index < 4; index++) {
    await expect(answerGroups(page).nth(index).getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "false");
  }
  await capture(page, "series-scope-just-this-result");

  await answerGroups(page).nth(1).getByRole("button", { name: "Going" }).click();
  dialog = page.getByRole("dialog", { name: /How far should “Going” apply/ });
  await dialog.getByRole("button", { name: "This and following dates" }).click();
  await expect(answerGroups(page).first().getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "true");
  for (let index = 1; index < 4; index++) {
    await expect(answerGroups(page).nth(index).getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
  }
  await expect(page.getByRole("status").filter({ hasText: "Going to 3 dates" })).toHaveCount(1);
  await capture(page, "series-scope-this-following-result");

  await answerGroups(page).nth(2).getByRole("button", { name: "Maybe" }).click();
  dialog = page.getByRole("dialog", { name: /How far should “Maybe” apply/ });
  await dialog.getByRole("button", { name: "All future dates" }).click();
  for (let index = 0; index < 4; index++) {
    await expect(answerGroups(page).nth(index).getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "true");
  }
  await expect(page.getByRole("status").filter({ hasText: "Going to 3 dates" })).toHaveCount(0);
  await expect(page.getByRole("status").filter({ hasText: "Maybe for 4 dates" })).toHaveCount(1);
  await capture(page, "series-scope-all-future-result");

  let confirmMessage = "";
  page.once("dialog", async (confirm) => {
    confirmMessage = confirm.message();
    await confirm.accept();
  });
  await page.getByRole("button", { name: "Can't go to any" }).first().click();
  expect(confirmMessage).toContain("removed from your calendar and personal feed");
  await expect(page.getByRole("status").filter({ hasText: /Can't go to 4 dates/ })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "Maybe for 4 dates" })).toHaveCount(0);
  for (let index = 0; index < 4; index++) {
    await expect(answerGroups(page).nth(index).getByRole("button", { name: "Can't go" })).toHaveAttribute("aria-pressed", "true");
  }
  evidence.push({ name: "bulk-cant-go-confirm", message: confirmMessage, accepted: true });
  await capture(page, "series-bulk-cant-go-result");

  const single = await createEvent(request, "Single date bypass browser proof", 8, "");
  await signInTo(page, "sam", `/events/${single.id}`);
  await answerGroups(page).getByRole("button", { name: "Maybe" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(answerGroups(page).getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "true");
  await capture(page, "single-date-bypass-result");
});

test("non-household sibling fields refresh after a series answer and Undo without stale save payloads", async ({ page, request }) => {
  test.setTimeout(180_000);
  const event = await createEvent(request, "Single RSVP refresh browser proof", 8, "FREQ=WEEKLY;COUNT=2", {
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10,
    childcareAvailable: true, childcareCapacity: 10,
  });
  const sam = await apiAs(request, "sam");
  const before = await sam("GET", `/events/${event.id}`) as { occurrences: { date: string }[] };
  for (const occurrence of before.occurrences) {
    await sam("PUT", `/events/${event.id}/registrations`, {
      date: occurrence.date, status: "going", attendanceMode: "in_person",
      guestCount: 0, childcareCount: 0, childcareAgesNote: "",
    });
  }

  await page.setViewportSize({ width: 390, height: 900 });
  await signInTo(page, "sam", `/events/${event.id}`);
  await expect(page.getByRole("form", { name: "Household answers" })).toHaveCount(0);
  await expect(answerGroups(page)).toHaveCount(2);
  const cards = page.locator(".bg-surface.rounded-lg.border-border");
  const first = cards.first();
  const second = cards.nth(1);

  await first.getByLabel("Extra guests").fill("2");
  await first.getByRole("radio", { name: "Remote" }).click();
  await expect(first.getByRole("status")).toContainText("Switched to remote");
  await first.getByLabel(/^Children needing childcare/).fill("2");
  await first.getByLabel(/^Ages/).fill("4 and 6");
  await first.getByRole("button", { name: "Save" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "All future dates" }).click();
  await expect(first.getByRole("button", { name: "Undo" })).toBeVisible();

  for (const card of [first, second]) {
    await expect(card.getByRole("radio", { name: "Remote" })).toBeChecked();
    await expect(card.getByLabel("Extra guests")).toHaveValue("2");
    await expect(card.getByLabel(/^Children needing childcare/)).toHaveValue("2");
    await expect(card.getByLabel(/^Ages/)).toHaveValue("4 and 6");
  }
  const applied = await sam("GET", `/events/${event.id}`) as {
    occurrences: { myAttendanceMode: string; myGuestCount: number; myChildcareCount: number; myChildcareAgesNote: string }[];
  };
  expect(applied.occurrences.map((row) => [row.myAttendanceMode, row.myGuestCount, row.myChildcareCount, row.myChildcareAgesNote]))
    .toEqual([["remote", 2, 2, "4 and 6"], ["remote", 2, 2, "4 and 6"]]);
  await capture(page, "single-rsvp-series-refreshed", true, [390]);

  await first.getByRole("button", { name: "Undo" }).click();
  await expect(first.getByRole("status")).toContainText("Undo:");
  await expect(second.getByRole("radio", { name: "In person" })).toBeChecked();
  await expect(second.getByLabel("Extra guests")).toHaveValue("0");
  await expect(second.getByLabel(/^Children needing childcare/)).toHaveValue("0");
  await expect(second.getByLabel(/^Ages/)).toHaveValue("");
  const undone = await sam("GET", `/events/${event.id}`) as {
    occurrences: { myAttendanceMode: string; myGuestCount: number; myChildcareCount: number; myChildcareAgesNote: string }[];
  };
  expect(undone.occurrences[1]).toMatchObject({
    myAttendanceMode: "in_person", myGuestCount: 0, myChildcareCount: 0, myChildcareAgesNote: "",
  });
  await capture(page, "single-rsvp-series-undone", true, [390]);

  const subsequentRequest = page.waitForRequest((request) => request.method() === "PUT"
    && request.url().endsWith(`/api/v1/events/${event.id}/registrations`));
  await second.getByRole("button", { name: "Save" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Just this date" }).click();
  expect((await subsequentRequest).postDataJSON()).toEqual({
    status: "going", date: before.occurrences[1].date, guestCount: 0, attendanceMode: "in_person",
    childcareCount: 0, childcareAgesNote: "",
  });
  await expect(second.getByRole("status")).toContainText("Saved: going, no childcare");
  const afterSave = await sam("GET", `/events/${event.id}`) as {
    occurrences: { myAttendanceMode: string; myGuestCount: number; myChildcareCount: number; myChildcareAgesNote: string }[];
  };
  expect(afterSave.occurrences[1]).toMatchObject({
    myAttendanceMode: "in_person", myGuestCount: 0, myChildcareCount: 0, myChildcareAgesNote: "",
  });
});

test("page-wide mutation serialization blocks sibling saves while committed Apply and household Undo responses are held", async ({ page, request }) => {
  test.setTimeout(240_000);
  await page.setViewportSize({ width: 390, height: 900 });

  const solo = await createEvent(request, "Held series response serialization proof", 27, "FREQ=WEEKLY;COUNT=2", {
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10,
    childcareAvailable: true, childcareCapacity: 10,
  });
  const sam = await apiAs(request, "sam");
  const soloDetail = await sam("GET", `/events/${solo.id}`) as { occurrences: { date: string }[] };
  for (const occurrence of soloDetail.occurrences) {
    await sam("PUT", `/events/${solo.id}/registrations`, {
      date: occurrence.date, status: "going", attendanceMode: "in_person",
      guestCount: 0, childcareCount: 0, childcareAgesNote: "",
    });
  }
  await signInTo(page, "sam", `/events/${solo.id}`);
  await expect(answerGroups(page)).toHaveCount(2);
  const cards = page.locator(".bg-surface.rounded-lg.border-border");
  const firstCard = cards.first();
  const siblingCard = cards.nth(1);
  await firstCard.getByRole("radio", { name: "Remote" }).click();
  await expect(firstCard.getByRole("status")).toContainText("Switched to remote");
  await firstCard.getByLabel("Extra guests").fill("2");
  await firstCard.getByLabel(/^Children needing childcare/).fill("2");
  await firstCard.getByLabel(/^Ages/).fill("4 and 6");

  let ordinaryRequests = 0;
  const countSoloOrdinary = (request: Request) => {
    if (request.method() === "PUT" && request.url().endsWith(`/api/v1/events/${solo.id}/registrations`)) ordinaryRequests += 1;
  };
  page.on("request", countSoloOrdinary);
  const heldApply = await holdNextPostResponse(page, `/api/v1/events/${solo.id}/series-registrations`);
  await firstCard.getByRole("button", { name: "Save" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "All future dates" }).click();
  await heldApply.committed;
  await expect(siblingCard.getByRole("button", { name: "Maybe", exact: true })).toBeDisabled();
  await expect(siblingCard.getByLabel("Extra guests")).toBeDisabled();
  await capture(page, "series-serialized-solo-apply-held-response", true, [390]);
  await siblingCard.getByRole("button", { name: "Maybe", exact: true }).evaluate((button: HTMLButtonElement) => button.click());
  await page.waitForTimeout(100);
  expect(ordinaryRequests).toBe(0);

  const heldApplyRefresh = await holdNextEventRefresh(page, solo.id);
  await heldApply.release();
  await heldApplyRefresh.started;
  await expect(siblingCard.getByRole("button", { name: "Maybe", exact: true })).toBeEnabled();
  await expect(siblingCard.getByRole("radio", { name: "Remote" })).toBeChecked();
  await expect(siblingCard.getByLabel("Extra guests")).toHaveValue("2");
  await expect(siblingCard.getByLabel(/^Children needing childcare/)).toHaveValue("2");
  await expect(siblingCard.getByLabel(/^Ages/)).toHaveValue("4 and 6");
  const soloSave = page.waitForRequest((request) => request.method() === "PUT"
    && request.url().endsWith(`/api/v1/events/${solo.id}/registrations`));
  await siblingCard.getByRole("button", { name: "Save" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Just this date" }).click();
  expect((await soloSave).postDataJSON()).toEqual({
    status: "going", date: soloDetail.occurrences[1].date, guestCount: 2, attendanceMode: "remote",
    childcareCount: 2, childcareAgesNote: "4 and 6",
  });
  await heldApplyRefresh.release();
  page.off("request", countSoloOrdinary);

  const household = await createEvent(request, "Held household Undo serialization proof", 29, "FREQ=WEEKLY;COUNT=2");
  const parent = await apiAs(request, "household-parent");
  const householdDetail = await parent("GET", `/events/${household.id}`) as { occurrences: { date: string }[] };
  await signInTo(page, "household-parent", `/events/${household.id}`);
  const forms = page.getByRole("form", { name: "Household answers" });
  await expect(forms).toHaveCount(2);
  const firstForm = forms.first();
  const siblingForm = forms.nth(1);
  await firstForm.getByRole("checkbox", { name: "Me" }).click();
  await firstForm.getByRole("checkbox", { name: "Mary" }).click();
  await firstForm.getByLabel("Answer for Mary").selectOption("maybe");
  await firstForm.getByRole("button", { name: "Going to all dates" }).click();
  await expect(firstForm.getByRole("button", { name: "Undo" })).toBeVisible();

  ordinaryRequests = 0;
  const countHouseholdOrdinary = (request: Request) => {
    if (request.method() === "PUT" && request.url().endsWith(`/api/v1/events/${household.id}/registrations`)) ordinaryRequests += 1;
  };
  page.on("request", countHouseholdOrdinary);
  const heldUndo = await holdNextPostResponse(page, `/api/v1/events/${household.id}/series-registrations/undo`);
  await firstForm.getByRole("button", { name: "Undo" }).click();
  await heldUndo.committed;
  await expect(siblingForm.getByRole("button", { name: "Save answers" })).toBeDisabled();
  await expect(siblingForm.getByRole("checkbox", { name: "Me" })).toBeDisabled();
  await expect(firstForm.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  await capture(page, "series-serialized-household-undo-held-response", true, [390]);
  await expect(firstForm.getByLabel("Answer for Mary")).toHaveValue("maybe");
  await siblingForm.getByRole("button", { name: "Save answers" }).evaluate((button: HTMLButtonElement) => button.click());
  await page.waitForTimeout(100);
  expect(ordinaryRequests).toBe(0);

  const heldUndoRefresh = await holdNextEventRefresh(page, household.id);
  await heldUndo.release();
  await heldUndoRefresh.started;
  await expect(siblingForm.getByRole("checkbox", { name: "Me" })).toBeEnabled();
  await expect(siblingForm.getByRole("checkbox", { name: "Me" })).not.toBeChecked();
  await expect(firstForm.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  await expect(firstForm.getByLabel("Answer for Mary")).toHaveValue("maybe");
  await siblingForm.getByRole("checkbox", { name: "Me" }).click();
  const householdSave = page.waitForRequest((request) => request.method() === "PUT"
    && request.url().endsWith(`/api/v1/events/${household.id}/registrations`));
  await siblingForm.getByRole("button", { name: "Save answers" }).click();
  expect((await householdSave).postDataJSON()).toEqual({
    date: householdDetail.occurrences[1].date, people: [], status: "going", attendanceMode: "in_person", guestCount: 0,
  });
  await heldUndoRefresh.release();
  page.off("request", countHouseholdOrdinary);
});

test("Apply and Undo publish authoritative sibling state before delayed parent GETs for solo and household editors", async ({ page, request }) => {
  test.setTimeout(240_000);
  await page.setViewportSize({ width: 390, height: 900 });

  const soloApply = await createEvent(request, "Solo delayed Apply proof", 30, "FREQ=WEEKLY;COUNT=2", {
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10,
    childcareAvailable: true, childcareCapacity: 10,
  });
  const sam = await apiAs(request, "sam");
  let detail = await sam("GET", `/events/${soloApply.id}`) as { occurrences: { date: string }[] };
  await sam("PUT", `/events/${soloApply.id}/registrations`, {
    date: detail.occurrences[0].date, status: "going", attendanceMode: "remote",
    guestCount: 2, childcareCount: 2, childcareAgesNote: "4 and 6",
  });
  await sam("PUT", `/events/${soloApply.id}/registrations`, {
    date: detail.occurrences[1].date, status: "going", attendanceMode: "in_person",
    guestCount: 0, childcareCount: 0, childcareAgesNote: "",
  });
  await signInTo(page, "sam", `/events/${soloApply.id}`);
  await expect(answerGroups(page)).toHaveCount(2);
  let cards = page.locator(".bg-surface.rounded-lg.border-border");
  let firstCard = cards.first();
  let siblingCard = cards.nth(1);
  const soloApplyRefresh = await holdNextEventRefresh(page, soloApply.id);
  await firstCard.getByRole("button", { name: "Save" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "All future dates" }).click();
  await soloApplyRefresh.started;
  await expect(siblingCard.getByRole("radio", { name: "Remote" })).toBeChecked();
  await expect(siblingCard.getByLabel("Extra guests")).toHaveValue("2");
  await expect(siblingCard.getByLabel(/^Children needing childcare/)).toHaveValue("2");
  await expect(siblingCard.getByLabel(/^Ages/)).toHaveValue("4 and 6");
  await capture(page, "series-atomic-solo-apply-held", true, [390]);
  let saveRequest = page.waitForRequest((request) => request.method() === "PUT"
    && request.url().endsWith(`/api/v1/events/${soloApply.id}/registrations`));
  await siblingCard.getByRole("button", { name: "Save" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Just this date" }).click();
  expect((await saveRequest).postDataJSON()).toEqual({
    status: "going", date: detail.occurrences[1].date, guestCount: 2, attendanceMode: "remote",
    childcareCount: 2, childcareAgesNote: "4 and 6",
  });
  await expect(siblingCard.getByRole("status")).toContainText("Saved: going");
  await soloApplyRefresh.release();
  await expect(siblingCard.getByRole("radio", { name: "Remote" })).toBeChecked();

  const soloUndo = await createEvent(request, "Solo delayed Undo proof", 33, "FREQ=WEEKLY;COUNT=2");
  await signInTo(page, "sam", `/events/${soloUndo.id}`);
  await expect(answerGroups(page)).toHaveCount(2);
  await page.getByRole("button", { name: "Maybe for all" }).first().click();
  await expect(page.getByRole("button", { name: "Undo" })).toBeVisible();
  const soloUndoRefresh = await holdNextEventRefresh(page, soloUndo.id);
  await page.getByRole("button", { name: "Undo" }).click();
  await soloUndoRefresh.started;
  const soloUndoSibling = answerGroups(page).nth(1);
  for (const name of ["Going", "Maybe", "Can't go"]) {
    await expect(soloUndoSibling.getByRole("button", { name })).toHaveAttribute("aria-pressed", "false");
  }
  saveRequest = page.waitForRequest((request) => request.method() === "PUT"
    && request.url().endsWith(`/api/v1/events/${soloUndo.id}/registrations`));
  await soloUndoSibling.getByRole("button", { name: "Maybe" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Just this date" }).click();
  const soloUndoDates = await sam("GET", `/events/${soloUndo.id}`) as { occurrences: { date: string }[] };
  expect((await saveRequest).postDataJSON()).toEqual({
    status: "maybe", date: soloUndoDates.occurrences[1].date, guestCount: 0,
  });
  await expect(soloUndoSibling.getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "true");
  await soloUndoRefresh.release();
  await expect(soloUndoSibling.getByRole("button", { name: "Maybe" })).toHaveAttribute("aria-pressed", "true");

  const householdApply = await createEvent(request, "Household delayed Apply proof", 36, "FREQ=WEEKLY;COUNT=2", {
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10,
    childcareAvailable: true, childcareCapacity: 10,
  });
  await signInTo(page, "household-parent", `/events/${householdApply.id}`);
  let forms = page.getByRole("form", { name: "Household answers" });
  await expect(forms).toHaveCount(2);
  let firstForm = forms.first();
  let siblingForm = forms.nth(1);
  await firstForm.getByRole("checkbox", { name: "Me" }).click();
  await firstForm.getByRole("group", { name: "How you will attend" }).getByRole("radio", { name: "Remote" }).click();
  await firstForm.getByLabel("Extra guests").fill("2");
  await firstForm.getByLabel(/^Other children needing childcare/).fill("2");
  await firstForm.getByLabel(/^Ages/).fill("4 and 6");
  await firstForm.getByRole("checkbox", { name: "Mary" }).click();
  await firstForm.getByLabel("Answer for Mary").selectOption("maybe");
  const householdApplyRefresh = await holdNextEventRefresh(page, householdApply.id);
  await firstForm.getByRole("button", { name: "Going to all dates" }).click();
  await householdApplyRefresh.started;
  await expect(siblingForm.getByRole("checkbox", { name: "Me" })).toBeChecked();
  await expect(siblingForm.getByRole("group", { name: "How you will attend" }).getByRole("radio", { name: "Remote" })).toBeChecked();
  await expect(siblingForm.getByLabel("Extra guests")).toHaveValue("2");
  await expect(siblingForm.getByLabel(/^Other children needing childcare/)).toHaveValue("2");
  await expect(siblingForm.getByLabel(/^Ages/)).toHaveValue("4 and 6");
  await expect(firstForm.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  await expect(firstForm.getByLabel("Answer for Mary")).toHaveValue("maybe");
  saveRequest = page.waitForRequest((request) => request.method() === "PUT"
    && request.url().endsWith(`/api/v1/events/${householdApply.id}/registrations`));
  await siblingForm.getByRole("button", { name: "Save answers" }).click();
  const householdApplyDates = await (await apiAs(request, "household-parent"))("GET", `/events/${householdApply.id}`) as { occurrences: { date: string }[] };
  expect((await saveRequest).postDataJSON()).toEqual({
    date: householdApplyDates.occurrences[1].date, people: [], status: "going", attendanceMode: "remote",
    guestCount: 2, childcareCount: 2, childcareAgesNote: "4 and 6",
  });
  await householdApplyRefresh.release();
  await expect(siblingForm.getByRole("group", { name: "How you will attend" }).getByRole("radio", { name: "Remote" })).toBeChecked();
  await expect(firstForm.getByRole("checkbox", { name: "Mary" })).toBeChecked();

  const householdUndo = await createEvent(request, "Household delayed Undo proof", 39, "FREQ=WEEKLY;COUNT=2", {
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10,
    childcareAvailable: true, childcareCapacity: 10,
  });
  await signInTo(page, "household-parent", `/events/${householdUndo.id}`);
  forms = page.getByRole("form", { name: "Household answers" });
  await expect(forms).toHaveCount(2);
  firstForm = forms.first();
  siblingForm = forms.nth(1);
  await firstForm.getByRole("checkbox", { name: "Me" }).click();
  await firstForm.getByRole("group", { name: "How you will attend" }).getByRole("radio", { name: "Remote" }).click();
  await firstForm.getByLabel("Extra guests").fill("2");
  await firstForm.getByLabel(/^Other children needing childcare/).fill("2");
  await firstForm.getByLabel(/^Ages/).fill("4 and 6");
  await firstForm.getByRole("checkbox", { name: "Mary" }).click();
  await firstForm.getByLabel("Answer for Mary").selectOption("maybe");
  await firstForm.getByRole("button", { name: "Going to all dates" }).click();
  await expect(firstForm.getByRole("button", { name: "Undo" })).toBeVisible();
  const householdUndoRefresh = await holdNextEventRefresh(page, householdUndo.id);
  await firstForm.getByRole("button", { name: "Undo" }).click();
  await householdUndoRefresh.started;
  await expect(siblingForm.getByRole("checkbox", { name: "Me" })).not.toBeChecked();
  await expect(firstForm.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  await expect(firstForm.getByLabel("Answer for Mary")).toHaveValue("maybe");
  await capture(page, "series-atomic-household-undo-held", true, [390]);
  await siblingForm.getByRole("checkbox", { name: "Me" }).click();
  saveRequest = page.waitForRequest((request) => request.method() === "PUT"
    && request.url().endsWith(`/api/v1/events/${householdUndo.id}/registrations`));
  await siblingForm.getByRole("button", { name: "Save answers" }).click();
  const householdUndoDates = await (await apiAs(request, "household-parent"))("GET", `/events/${householdUndo.id}`) as { occurrences: { date: string }[] };
  expect((await saveRequest).postDataJSON()).toEqual({
    date: householdUndoDates.occurrences[1].date, people: [], status: "going", attendanceMode: "in_person",
    guestCount: 0, childcareCount: 0, childcareAgesNote: "",
  });
  await householdUndoRefresh.release();
  await expect(siblingForm.getByRole("checkbox", { name: "Me" })).toBeChecked();
  await expect(siblingForm.getByRole("group", { name: "How you will attend" }).getByRole("radio", { name: "In person" })).toBeChecked();
});

test("a household user keeps one-date drafts while their own answer uses every series scope", async ({ page, request }) => {
  test.setTimeout(180_000);
  const event = await createEvent(request, "Household series browser proof", 9, "FREQ=WEEKLY;COUNT=4", {
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10,
    childcareAvailable: true, childcareCapacity: 10,
  });

  await page.setViewportSize({ width: 390, height: 900 });
  await signInTo(page, "household-parent", `/events/${event.id}`);
  const forms = page.getByRole("form", { name: "Household answers" });
  await expect(forms).toHaveCount(4);
  const first = forms.first();
  await first.getByRole("checkbox", { name: "Me" }).click();
  await first.getByLabel("Answer for you").selectOption("maybe");
  await first.getByRole("checkbox", { name: "Mary" }).click();
  await first.getByLabel("Answer for Mary").selectOption("maybe");
  await first.getByLabel("Extra guests").fill("1");
  await first.getByLabel(/^Other children needing childcare/).fill("1");
  await first.getByLabel(/^Ages/).fill("4");

  await first.getByRole("button", { name: "Choose dates for my answer" }).click();
  await expect(page.getByRole("dialog", { name: /How far should “Maybe” apply/ })).toBeVisible();
  await capture(page, "household-series-scope-dialog", false);
  await page.getByRole("button", { name: "Just this date" }).click();
  await expect(first.getByRole("status").filter({ hasText: "You: maybe" })).toBeVisible();
  await expect(first.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  await expect(first.getByLabel("Answer for Mary")).toHaveValue("maybe");

  const second = forms.nth(1);
  await second.getByRole("checkbox", { name: "Me" }).click();
  await second.getByLabel("Extra guests").fill("2");
  await second.getByLabel(/^Other children needing childcare/).fill("1");
  await second.getByLabel(/^Ages/).fill("5");
  await second.getByRole("button", { name: "Choose dates for my answer" }).click();
  await page.getByRole("button", { name: "This and following dates" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Going to 3 dates" })).toHaveCount(1);
  await expect(first.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  const parent = await apiAs(request, "household-parent");
  const afterFollowing = await parent("GET", `/events/${event.id}`) as {
    occurrences: { myRegistration: string | null; myGuestCount: number; myChildcareCount: number; myChildcareAgesNote: string; myHouseholdRegistrations?: unknown[] }[];
  };
  expect(afterFollowing.occurrences.slice(1).map((row) => [row.myRegistration, row.myGuestCount, row.myChildcareCount, row.myChildcareAgesNote]))
    .toEqual([["going", 2, 1, "5"], ["going", 2, 1, "5"], ["going", 2, 1, "5"]]);
  expect(afterFollowing.occurrences.flatMap((row) => row.myHouseholdRegistrations ?? [])).toEqual([]);

  await first.getByRole("group", { name: "How you will attend" }).getByRole("radio", { name: "Remote" }).click();
  await first.getByLabel("Extra guests").fill("2");
  await first.getByLabel(/^Other children needing childcare/).fill("2");
  await first.getByLabel(/^Ages/).fill("unsaved draft");
  await first.getByRole("button", { name: "Maybe for all" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Maybe for 4 dates" })).toHaveCount(1);
  const refreshed = await parent("GET", `/events/${event.id}`) as {
    occurrences: { myRegistration: string | null; myAttendanceMode: "in_person" | "remote"; myGuestCount: number; myChildcareCount: number; myChildcareAgesNote: string }[];
  };
  for (let index = 0; index < 4; index++) {
    const form = forms.nth(index);
    const server = refreshed.occurrences[index];
    await expect(form.getByRole("checkbox", { name: "Me" })).toBeChecked();
    await expect(form.getByLabel("Answer for you")).toHaveValue(server.myRegistration);
    await expect(form.getByRole("group", { name: "How you will attend" }).getByRole("radio", { name: server.myAttendanceMode === "remote" ? "Remote" : "In person" })).toBeChecked();
    await expect(form.getByLabel("Extra guests")).toHaveValue(String(server.myGuestCount));
    await expect(form.getByLabel(/^Other children needing childcare/)).toHaveValue(String(server.myChildcareCount));
    await expect(form.getByLabel(/^Ages/)).toHaveValue(server.myChildcareAgesNote);
  }
  await expect(first.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  await expect(first.getByLabel("Answer for Mary")).toHaveValue("maybe");
  await capture(page, "household-series-r4-synchronized-forms");

  const formerlyUnanswered = forms.nth(2);
  await formerlyUnanswered.getByRole("checkbox", { name: "Mary" }).click();
  await formerlyUnanswered.getByLabel("Answer for Mary").selectOption("maybe");
  await formerlyUnanswered.getByRole("button", { name: "Save answers" }).click();
  await expect(formerlyUnanswered.getByRole("status").filter({ hasText: "You: maybe" })).toBeVisible();
  await expect(formerlyUnanswered.getByRole("status").filter({ hasText: "Mary: maybe" })).toBeVisible();

  const detail = await parent("GET", `/events/${event.id}`) as {
    occurrences: { myRegistration: string | null; myGuestCount: number; myChildcareCount: number; myChildcareAgesNote: string; myHouseholdRegistrations?: { status: string }[] }[];
  };
  expect(detail.occurrences.map((row) => row.myRegistration)).toEqual(["maybe", "maybe", "maybe", "maybe"]);
  expect(detail.occurrences[2].myHouseholdRegistrations?.map((row) => row.status)).toEqual(["maybe"]);
});

test("unrelated household refreshes preserve drafts and a no-op actor bulk refresh reconciles server truth", async ({ page, request }) => {
  test.setTimeout(120_000);
  const event = await createEvent(request, "Household refresh boundary browser proof", 11, "FREQ=WEEKLY;COUNT=2", {
    guestsAllowed: 2, remoteAvailable: true, remoteCapacity: 10,
    childcareAvailable: true, childcareCapacity: 10,
  });
  const parent = await apiAs(request, "household-parent");
  const detail = await parent("GET", `/events/${event.id}`) as { occurrences: { date: string }[] };
  await parent("PUT", `/events/${event.id}/registrations`, {
    date: detail.occurrences[0].date, status: "going", attendanceMode: "in_person",
    guestCount: 0, childcareCount: 0, childcareAgesNote: "", people: [],
  });

  await page.setViewportSize({ width: 390, height: 900 });
  await signInTo(page, "household-parent", `/events/${event.id}`);
  const forms = page.getByRole("form", { name: "Household answers" });
  await expect(forms).toHaveCount(2);
  const first = forms.first();
  const second = forms.nth(1);

  await first.getByLabel("Answer for you").selectOption("maybe");
  await first.getByRole("group", { name: "How you will attend" }).getByRole("radio", { name: "Remote" }).click();
  await first.getByLabel("Extra guests").fill("2");
  await first.getByLabel(/^Other children needing childcare/).fill("2");
  await first.getByLabel(/^Ages/).fill("4 and 6");
  await first.getByRole("checkbox", { name: "Mary" }).click();
  await first.getByLabel("Answer for Mary").selectOption("maybe");

  await second.getByRole("checkbox", { name: "Mary" }).click();
  await second.getByLabel("Answer for Mary").selectOption("maybe");
  await second.getByRole("button", { name: "Save answers" }).click();
  await expect(second.getByRole("status").filter({ hasText: "Mary: maybe" })).toBeVisible();

  await expect(first.getByLabel("Answer for you")).toHaveValue("maybe");
  await expect(first.getByRole("group", { name: "How you will attend" }).getByRole("radio", { name: "Remote" })).toBeChecked();
  await expect(first.getByLabel("Extra guests")).toHaveValue("2");
  await expect(first.getByLabel(/^Other children needing childcare/)).toHaveValue("2");
  await expect(first.getByLabel(/^Ages/)).toHaveValue("4 and 6");
  await expect(first.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  await expect(first.getByLabel("Answer for Mary")).toHaveValue("maybe");

  await second.getByRole("checkbox", { name: "Me" }).click();
  await second.getByRole("button", { name: "Going to all dates" }).click();
  await expect(page.getByRole("status").filter({ hasText: "Going to 2 dates" })).toBeVisible();
  await expect(first.getByLabel("Answer for you")).toHaveValue("going");
  await expect(first.getByRole("group", { name: "How you will attend" }).getByRole("radio", { name: "In person" })).toBeChecked();
  await expect(first.getByLabel("Extra guests")).toHaveValue("0");
  await expect(first.getByLabel(/^Other children needing childcare/)).toHaveValue("0");
  await expect(first.getByLabel(/^Ages/)).toHaveValue("");
  await expect(first.getByRole("checkbox", { name: "Mary" })).toBeChecked();
  await expect(first.getByLabel("Answer for Mary")).toHaveValue("maybe");
  await capture(page, "household-refresh-boundary", true, [390]);
});

test("terminal Undo after authority loss refreshes away protected event details", async ({ page, request }) => {
  test.setTimeout(120_000);
  const admin = await apiAs(request, "grier");
  const { users } = await admin("GET", "/admin/users") as { users: { id: number; email: string }[] };
  const sam = users.find((user) => user.email === "sam@example.com");
  expect(sam).toBeTruthy();
  const { group } = await admin("POST", "/groups", {
    name: "Undo authority browser proof", description: "", tags: [], location: "", visibility: "invite_only", memberLimit: null,
  }) as { group: { id: number } };
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: sam!.id, role: "member" });
  const { event } = await admin("POST", `/groups/${group.id}/events`, {
    title: "Protected Undo browser proof", startsLocal: `${day(15)}T18:00`, durationMinutes: 60,
    tzid: "UTC", recurrenceRule: "FREQ=WEEKLY;COUNT=2", visibility: "invite_only",
  }) as { event: { id: number } };

  await signInTo(page, "sam", `/events/${event.id}`);
  await page.getByRole("button", { name: "Maybe for all" }).first().click();
  await expect(page.getByRole("button", { name: "Undo" })).toBeVisible();

  const { departure } = await admin("GET", `/groups/${group.id}/members/${sam!.id}/departure-preview`) as { departure: { snapshot: string } };
  await admin("POST", `/groups/${group.id}/members/${sam!.id}/departure`, { snapshot: departure.snapshot, keepOccurrenceIds: [] });
  let releaseRefresh!: () => void;
  let markRefreshStarted!: () => void;
  const refreshStarted = new Promise<void>((resolve) => { markRefreshStarted = resolve; });
  const refreshRelease = new Promise<void>((resolve) => { releaseRefresh = resolve; });
  const eventRoute = `**/api/v1/events/${event.id}`;
  await page.route(eventRoute, async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    markRefreshStarted();
    await refreshRelease;
    await route.continue();
  });
  await page.getByRole("button", { name: "Undo" }).click();
  await refreshStarted;
  await expect(page.getByRole("heading", { name: "Protected Undo browser proof" })).toHaveCount(0);
  releaseRefresh();
  await expect(page.getByRole("heading", { name: "Event not found" })).toBeVisible();
  await page.unroute(eventRoute);
});

test("mixed results, Undo seat loss, and Undo expiry are visible in the real browser", async ({ page, request }) => {
  test.setTimeout(180_000);
  const mixed = await createEvent(request, "Mixed series outcome browser proof", 12, "FREQ=WEEKLY;COUNT=3", { capacity: 1, waitlistEnabled: true });
  let dates = await eventDates(request, mixed.id);
  await answerDate(request, "sam", mixed.id, dates[2].date, "going");
  dates = await eventDates(request, mixed.id);
  const removed = dates[2].attendees.find((attendee) => attendee.registrationId);
  expect(removed).toBeTruthy();
  const admin = await apiAs(request, "grier");
  await admin("POST", `/events/${mixed.id}/registrations/remove`, { date: dates[2].date, registrationId: removed!.registrationId });
  await answerDate(request, "lee", mixed.id, dates[1].date, "going");

  await page.setViewportSize({ width: 390, height: 900 });
  await signInTo(page, "sam", `/events/${mixed.id}`);
  await answerGroups(page).first().getByRole("button", { name: "Going" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "All future dates" }).click();
  const mixedStatus = page.getByRole("status").filter({ hasText: /Going to 1 date/ });
  await expect(mixedStatus).toContainText(`waitlisted for ${formatCalendarDay(dates[1].date)}`);
  await expect(mixedStatus).not.toContainText(dates[1].date);
  await expect(mixedStatus).toContainText("1 date was left unchanged");
  await expect(answerGroups(page).first().getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
  await expect(answerGroups(page).nth(1).getByRole("button", { name: "Going" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByText(/Waitlisted/).nth(0)).toBeVisible();
  await expect(page.getByText(/removed your registration/)).toBeVisible();
  await capture(page, "series-mixed-outcome-result");

  const seatLoss = await createEvent(request, "Undo seat loss browser proof", 20, "FREQ=WEEKLY;COUNT=1", { capacity: 1, waitlistEnabled: true });
  const seatDate = (await eventDates(request, seatLoss.id))[0].date;
  await answerDate(request, "sam", seatLoss.id, seatDate, "going");
  await answerDate(request, "lee", seatLoss.id, seatDate, "going");
  await signInTo(page, "sam", `/events/${seatLoss.id}`);
  await answerGroups(page).getByRole("button", { name: "Maybe" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "All future dates" }).click();
  await expect(page.getByRole("button", { name: "Undo" })).toBeVisible();
  await page.getByRole("button", { name: "Undo" }).click();
  const undoStatus = page.getByRole("status").filter({ hasText: /Undo:/ });
  await expect(undoStatus).toContainText(`waitlisted for ${formatCalendarDay(seatDate)}`);
  await expect(undoStatus).toContainText(`a seat was no longer available for ${formatCalendarDay(seatDate)}`);
  await expect(undoStatus).not.toContainText(seatDate);
  await expect(page.getByText(/Waitlisted/).first()).toBeVisible();
  await capture(page, "series-undo-seat-loss-result");

  const expiry = await createEvent(request, "Undo expiry browser proof", 24, "FREQ=WEEKLY;COUNT=1");
  // The Undo window is a client setTimeout from the server's undoExpiresAt (RsvpButtons.tsx), so a fake clock
  // that jumps past it proves the same expiry without waiting 10 real seconds.
  await page.clock.install();
  await signInTo(page, "sam", `/events/${expiry.id}`);
  await answerGroups(page).getByRole("button", { name: "Maybe" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "All future dates" }).click();
  await expect(page.getByRole("button", { name: "Undo" })).toBeVisible();
  await capture(page, "series-undo-available-result");
  await page.clock.fastForward(11_000);
  await expect(page.getByRole("status")).toContainText("The 10-second Undo time has ended. You can change your answers again from the event.");
  await expect(page.getByRole("button", { name: "Undo" })).toHaveCount(0);
  await capture(page, "series-undo-expired-result");

  if (proof) {
    writeFileSync(`${proof}/result.txt`, [
      "feature=series-rsvp-browser-proof",
      "entry-points=event date RSVP segments; series-wide answer buttons",
      "scopes=just_this_date,this_and_following,all_future",
      "bulk-cant-go=confirmed",
      "mixed-outcomes=going,waitlisted,removed-refusal",
      "undo=success-with-seat-loss,expiry-message",
      "single-date=dialog-bypassed",
      "household=account-only-scopes-with-proxy-drafts-preserved",
      "single-rsvp-refresh=remote-guests-childcare-payload-and-undo",
      "terminal-undo=protected-parent-refreshed-away",
      "summary-dates=human-readable-no-iso",
      "widths=390,768,1280",
    ].join("\n") + "\n");
  }
});
