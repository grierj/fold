import { expect, test } from "@playwright/test";
import { apiAs, email, fromMore, PASSWORD, signInTo, watchCSP } from "./helpers";

// The composer (M3.4c, PLAN 7.3): a leader writes to their group from its
// page, sees the live count, previews it, sends a test to themselves,
// schedules it for tomorrow, finds it on Messages and cancels it.
test("a leader emails their group: write, count, preview, test, schedule, cancel", async ({ page, request }) => {
  const csp = await watchCSP(page);
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const room = groups.find((g) => g.name === "Room 201 class")!;
  const subject = `Snacks this week ${Date.now()}`;

  await signInTo(page, "lee", `/groups/${room.id}`);
  await fromMore(page, "Email members", "More group actions");
  await expect(page).toHaveURL(new RegExp(`/messages/new\\?group=${room.id}$`));
  await expect(page.getByRole("heading", { name: "New message", level: 1 })).toBeVisible();
  await expect(page.getByRole("combobox", { name: "Send to" })).toHaveValue(`group:${room.id}`);
  // The live count, after consent filtering.
  await expect(page.getByRole("status").filter({ hasText: /^\d+ (people|person);/ })).toBeVisible();

  await page.getByRole("textbox", { name: "Subject" }).fill(subject);
  const body = page.getByRole("textbox", { name: "Message" });
  await body.click();
  await page.keyboard.type("Hi ");
  await page.getByRole("button", { name: "Insert field" }).click();
  await page.getByRole("menuitem", { name: "First name" }).click();
  await expect(body).toBeFocused();
  await page.keyboard.type(", please ");
  await page.getByRole("button", { name: "Bold" }).click();
  await expect(page.getByRole("button", { name: "Bold" })).toHaveAttribute("aria-pressed", "true");
  await expect(body).toBeFocused();
  await page.keyboard.type("bring snacks");
  await expect(body.locator("strong")).toHaveText("bring snacks");
  await expect(body).toContainText("Hi {{first_name}}, please bring snacks");

  await page.getByRole("button", { name: "Preview" }).click();
  await expect(page).toHaveURL(/\/messages\/\d+$/);
  const frame = page.frameLocator('iframe[title="Email preview"]');
  await expect(frame.getByText("Hi Lee, please")).toBeVisible();
  await expect(frame.locator("strong")).toHaveText("bring snacks");
  await page.getByText("Plain-text version").click();
  await expect(page.locator("pre")).toContainText("Hi Lee, please bring snacks");

  // A test needs a mail sender: dev mode's outbox (E2E_APP_ENV=dev). In
  // production mode with no provider the page says so in words instead.
  const outbox = (await page.request.get("/dev/outbox")).status() === 200;
  await page.getByRole("button", { name: "Send a test to me" }).click();
  if (outbox) {
    await expect(page.getByText("Sent to you at lee@example.com.")).toBeVisible();
    expect(await (await page.request.get("/dev/outbox")).text()).toContain(`[Test] ${subject}`);
  } else {
    await expect(page.getByRole("alert").filter({ hasText: "We couldn't send the test." })).toBeVisible();
  }

  // Tomorrow at 10:00 on the church's clock (America/Boise).
  const tomorrow = new Intl.DateTimeFormat("en-CA", { timeZone: "America/Boise", year: "numeric", month: "2-digit", day: "2-digit" }).format(Date.now() + 864e5);
  await page.getByRole("radio", { name: "Pick a date and time" }).check();
  await page.getByLabel("Date", { exact: true }).fill(tomorrow);
  await page.getByLabel("Time", { exact: true }).fill("10:00");
  await page.getByRole("button", { name: "Schedule" }).click();
  const ask = page.getByRole("group", { name: /^Send to \d+ (people|person) / });
  await expect(ask).toBeVisible();
  await ask.getByRole("button", { name: "Schedule" }).click();
  await expect(page.getByText(/^Scheduled for /)).toBeVisible();
  await expect(page.getByRole("button", { name: "Edit" })).toBeVisible();

  await page.getByRole("link", { name: "Back to messages" }).click();
  const row = page.getByRole("link").filter({ hasText: subject });
  await expect(row).toContainText(/^Scheduled for /);
  await expect(row).toContainText("To Room 201 class (Members, Leaders)");

  await row.click();
  page.once("dialog", (d) => void d.accept());
  await page.getByRole("button", { name: "Cancel message" }).click();
  await expect(page.getByText("Cancelled", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Edit" })).toHaveCount(0);
  expect(csp).toEqual([]);
});

// Sending (M3.5) and its history (M3.5b): Send now goes out within a few
// seconds and the page follows it without a reload. In production mode the
// e2e server has no email service, so the send fails in words, with Try
// again; in dev mode (E2E_APP_ENV=dev) the outbox takes it and the page
// says who it reached.
test("a leader sends now and the page follows it: sent with counts, or failed with Try again", async ({ page, request }) => {
  const csp = await watchCSP(page);
  const admin = await apiAs(request, "grier");
  const { groups } = (await admin("GET", "/groups")) as { groups: { id: number; name: string }[] };
  const room = groups.find((g) => g.name === "Room 201 class")!;
  const subject = `Room change ${Date.now()}`;

  await signInTo(page, "lee", `/messages/new?group=${room.id}`);
  await expect(page.getByRole("status").filter({ hasText: /^\d+ (people|person);/ })).toBeVisible();
  await page.getByRole("textbox", { name: "Subject" }).fill(subject);
  await page.getByRole("textbox", { name: "Message" }).click();
  await page.keyboard.type("We're meeting in Room 104 this week.");
  await page.getByRole("button", { name: "Send now" }).click();
  await page.getByRole("group", { name: /^Send to \d+ (people|person) now\?$/ }).getByRole("button", { name: "Send now" }).click();

  const dev = (await page.request.get("/dev/outbox")).status() === 200;
  // The page checks every 5 seconds while it waits and sends.
  if (dev) {
    await expect(page.getByText(/^Sent to \d+ (people|person)/).first()).toBeVisible({ timeout: 20_000 });
    await expect(page.getByText(/^Sent (Today|Yesterday)/)).toBeVisible();
    await expect(page.getByRole("button", { name: "Try again" })).toHaveCount(0);
    await page.getByText("See who got it").click();
    await expect(page.getByRole("listitem").filter({ hasText: "· Sent" }).first()).toBeVisible();
    expect(await (await page.request.get("/dev/outbox")).text()).toContain(subject);
  } else {
    const banner = page.getByRole("alert").filter({ hasText: "This message didn't go out" });
    const again = page.getByRole("button", { name: "Try again" });
    await expect(banner).toBeVisible({ timeout: 20_000 });
    await expect(banner).toContainText("No email service is set up yet");
    await expect(page.getByText("Failed", { exact: true })).toBeVisible();
    await expect(page.getByText(/^\d+ didn't get it/)).toBeVisible();
    await page.getByText("See who got it").click();
    await expect(page.getByRole("listitem").filter({ hasText: "· Didn't get it" }).first()).toBeVisible();
    // Try again asks, sends to the same people, and fails the same way.
    const asked: string[] = [];
    page.once("dialog", (d) => { asked.push(d.message()); void d.accept(); });
    await again.click();
    await expect.poll(() => asked).toEqual([expect.stringMatching(/^Send again to the \d+ (people|person) it didn't reach\?$/)]);
    await expect(page.getByText(/^Sending again… \d+ of \d+ so far$/)).toBeVisible();
    await expect(banner).toHaveCount(0);
    await expect(banner).toBeVisible({ timeout: 20_000 });
    await expect(again).toBeVisible();
    await expect(page.getByText("Failed", { exact: true })).toBeVisible();
  }

  // The list says the same in words.
  await page.getByRole("link", { name: "Back to messages" }).click();
  await expect(page.getByRole("link").filter({ hasText: subject })).toContainText(dev ? /^Sent / : /^Failed/);
  expect(csp).toEqual([]);
});

// "Push notification too" (M3.13d2, DEC-123): on by default, the summary
// counts who also gets a push, the list marks them in words, and turning
// it off is saved with the draft. Its own group and member, so no other
// spec's people get a device; nothing is sent, so no push leaves.
test("Push notification too is on by default, counted, and saved off", async ({ page, request }) => {
  const csp = await watchCSP(page);
  const stamp = Date.now() % 100000;
  const login = `push-reader-${stamp}`;
  const admin = await apiAs(request, "grier");
  const { users } = (await admin("GET", "/admin/users")) as { users: { id: number; email: string }[] };
  const lee = users.find((u) => u.email === email("lee"))!;
  const { user } = await admin("POST", "/admin/users", { ageBand: "adult", email: email(login), name: "Pia Pusher", password: PASSWORD, role: "member" });
  const { group } = await admin("POST", "/groups", {
    name: `Push check ${stamp}`, description: "For the push switch.", tags: [], imageUrl: "", location: "", visibility: "public", memberLimit: null,
  });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: lee.id, role: "leader" });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: user.id, role: "member" });
  const pia = await apiAs(request, login);
  const endpoint = `https://fcm.googleapis.com/fcm/send/e2e-composer-${stamp}`;
  await pia("PUT", "/push/subscriptions", { endpoint, expirationTime: null, keys: { p256dh: RFC_P256DH, auth: RFC_AUTH } });
  try {
    await signInTo(page, "lee", `/messages/new?group=${group.id}`);
    const box = page.getByRole("checkbox", { name: /Push notification too/ });
    await expect(box).toBeChecked();
    await expect(page.getByRole("status").filter({ hasText: /^\d+ (people|person);/ })).toBeVisible();
    await expect(page.getByText("1 also gets a push notification.")).toBeVisible();
    await page.getByText("See who's on the list").click();
    await expect(page.getByRole("listitem").filter({ hasText: "Pia Pusher" })).toContainText("Push too");
    await expect(page.getByRole("listitem").filter({ hasText: "Lee Leader" })).not.toContainText("Push too");

    await box.uncheck();
    await expect(page.getByText("1 also gets a push notification.")).toHaveCount(0);
    await expect(page.getByText("Push too")).toHaveCount(0);
    await page.getByRole("textbox", { name: "Subject" }).fill(`Push off ${stamp}`);
    await page.getByRole("button", { name: "Save draft" }).click();
    await expect(page.getByText("Draft saved.")).toBeVisible();
    await expect(page).toHaveURL(/\/messages\/\d+$/);
    await page.reload();
    await expect(page.getByRole("checkbox", { name: /Push notification too/ })).not.toBeChecked();
    expect(csp).toEqual([]);
  } finally {
    await (await apiAs(request, login))("DELETE", "/push/subscriptions", { endpoint });
  }
});

// The RFC 8291 Appendix A browser keys (push.spec.ts): valid, and never
// sent anywhere here.
const RFC_P256DH = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4";
const RFC_AUTH = "BTBZMqHH6r4Tts7J_aSIgg";
