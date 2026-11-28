import { expect, test } from "@playwright/test";
import { gate, watchCSP } from "./gates";
import { latestLink, signIn } from "./session";

// Slice 1 pages through the gates. Runs after slice0.spec.ts, on the same
// site: the church is set up and Pat is its first admin.

const admin = "pat@cloverdale.example";

test("account requests and notices, through the gates", async ({ page, browser }) => {
  await watchCSP(page);
  const visitor = await (await browser.newContext()).newPage();
  await watchCSP(visitor);

  await visitor.goto("/request");
  await gate(visitor, "request form");
  await visitor.getByRole("button", { name: "Ask for an account" }).click();
  await expect(visitor.getByText("Check these, then try again")).toBeVisible();
  await gate(visitor, "request form errors");
  await visitor.getByLabel("Your name").fill("Young Person");
  await visitor.getByLabel("Your email").fill("young@example.org");
  await visitor.getByLabel("I am under 18").check();
  await visitor.getByRole("button", { name: "Ask for an account" }).click();
  await expect(visitor.getByRole("heading", { name: "Accounts for under-18s aren't open yet" })).toBeVisible();
  await gate(visitor, "request under 18");

  await visitor.goto("/request");
  await visitor.getByLabel("Your name").fill("Sam Member");
  await visitor.getByLabel("Your email").fill("sam@example.org");
  await visitor.getByLabel("I am 18 or older").check();
  await visitor.getByRole("button", { name: "Ask for an account" }).click();
  await expect(visitor.getByRole("heading", { name: "Thanks for asking" })).toBeVisible();
  await gate(visitor, "request sent");

  await signIn(page, admin);
  await expect(page.getByRole("heading", { name: /^Good (morning|afternoon|evening), Pat Admin$/ })).toBeVisible();
  await expect(page.locator(".site-nav .badge")).toHaveText(/1/);
  await page.goto("/notices");
  await expect(page.getByText("Sam Member asked for an account.")).toBeVisible();
  await gate(page, "notices");
  await page.getByRole("button", { name: /Sam Member asked for an account/ }).click();
  await expect(page.getByRole("heading", { name: "Account requests" })).toBeVisible();
  await gate(page, "admin requests");
  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByText("Approved. They've been sent a sign-in link.")).toBeVisible();
  await gate(page, "admin requests empty");
  await page.goto("/notices");
  await page.getByRole("button", { name: "Mark all as read" }).isHidden();
  await gate(page, "notices read");

  // The visitor's browser asked, so the approval link signs them in at once.
  await visitor.goto(await latestLink(page, "sam@example.org"));
  await expect(visitor.getByRole("heading", { name: /^Good (morning|afternoon|evening), Sam Member$/ })).toBeVisible();
  await visitor.goto("/notices");
  await gate(visitor, "notices empty");

  // Sam starts a group; it waits; Pat approves it.
  await visitor.goto("/groups");
  await gate(visitor, "groups empty");
  await visitor.getByRole("link", { name: "Start a group" }).first().click();
  await gate(visitor, "group form");
  await visitor.getByRole("button", { name: "Start the group" }).click();
  await expect(visitor.getByText("Check these, then try again")).toBeVisible();
  await gate(visitor, "group form errors");
  await visitor.getByLabel("Name").fill("Tuesday Bible Study");
  await visitor.getByLabel("About the group").fill("We read a chapter a week.\nCoffee provided.");
  await visitor.getByLabel("Church only").check();
  await visitor.getByRole("button", { name: "Start the group" }).click();
  await expect(visitor.getByText("Waiting for approval")).toBeVisible();
  await gate(visitor, "group waiting");

  await page.goto("/admin/groups");
  await gate(page, "admin groups");
  await page.locator("summary", { hasText: "Turn it down" }).click();
  await gate(page, "admin groups reject open");
  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByText("Approved. Its leaders have been told.")).toBeVisible();

  await visitor.goto("/notices");
  await expect(visitor.getByText(/was approved/)).toBeVisible();
  await visitor.getByRole("button", { name: /was approved/ }).click();
  await expect(visitor.getByRole("heading", { name: "Tuesday Bible Study" })).toBeVisible();
  await gate(visitor, "group leader");
  await visitor.getByRole("link", { name: "Edit" }).click();
  await gate(visitor, "group edit");
  await visitor.getByLabel("Invite only").check();
  await visitor.getByRole("button", { name: "Save" }).click();
  await expect(visitor.getByText("Nobody is removed.")).toBeVisible();
  await gate(visitor, "group more private");
  await visitor.getByRole("button", { name: "Make it Invite only" }).click();
  await expect(visitor.getByText("Saved.")).toBeVisible();

  // Pat, an admin, sees and joins it; the list and home show it.
  await page.goto("/groups");
  await gate(page, "groups list");
  await page.getByRole("link", { name: "Tuesday Bible Study" }).click();
  await gate(page, "group admin view");
  await page.getByRole("button", { name: "Join" }).click();
  await expect(page.getByText("You joined the group.")).toBeVisible();
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Your groups" })).toBeVisible();
  await gate(page, "home with groups");

  // Sam, the leader, sees the roster, makes Pat a co-leader, searches and invites.
  await visitor.goto("/groups");
  await visitor.getByRole("link", { name: "Tuesday Bible Study" }).click();
  await visitor.getByRole("link", { name: /Members/ }).click();
  await expect(visitor.getByRole("heading", { name: "Members" })).toBeVisible();
  await gate(visitor, "roster leader");
  await visitor.getByRole("button", { name: "Make a leader" }).click();
  await expect(visitor.getByText("They're now a leader.")).toBeVisible();
  await visitor.getByLabel("Add someone by name").fill("Nobody Here");
  await visitor.getByRole("button", { name: "Find" }).click();
  await expect(visitor.getByText("Nobody by that name")).toBeVisible();
  await gate(visitor, "roster search");
  await visitor.getByRole("button", { name: "Send the invitation" }).click();
  await expect(visitor.getByText("Check these, then try again")).toBeVisible();
  await gate(visitor, "invite errors");
  await visitor.getByLabel("Their name").fill("Nia New");
  await visitor.getByLabel("Their email").fill("nia@example.org");
  await visitor.getByLabel("They are 18 or older").check();
  await visitor.getByRole("button", { name: "Send the invitation" }).click();
  await expect(visitor.getByText("Invitation sent.")).toBeVisible();
  await gate(visitor, "roster after invite");

  // Nia opens her emailed invitation in her own browser, says she is an
  // adult and accepts (R32).
  await page.goto("/dev/outbox");
  const niaText = await page.locator("li", { hasText: "To nia@example.org" }).first().locator("[data-outbox-text]").innerText();
  const niaLink = niaText.match(/http:\/\/[^\s]+\/invite\/[A-Za-z0-9_-]{43}/)![0];
  const nia = await (await browser.newContext()).newPage();
  await watchCSP(nia);
  await nia.goto(niaLink);
  await expect(nia.getByRole("heading", { name: "Join Tuesday Bible Study" })).toBeVisible();
  await gate(nia, "invitation by email");
  await nia.getByLabel("I am 18 or older").check();
  await nia.getByRole("button", { name: "Accept the invitation" }).click();
  await expect(nia.getByText("You joined the group.")).toBeVisible();

  // Sam's profile: the email switch saves when changed.
  await visitor.goto("/profile");
  await gate(visitor, "profile");
  await visitor.getByRole("button", { name: "Show my email to leaders of my groups" }).click();
  await expect(visitor.locator("#contact").getByRole("status")).toHaveText(/Saved/);
  await expect(visitor.getByRole("button", { name: "Show my email to leaders of my groups" })).toHaveAttribute("aria-pressed", "false");

  // Pat finds Nia among the people and turns her account off, then on.
  await page.goto("/admin/people");
  await gate(page, "admin people");
  await page.getByRole("link", { name: "Nia New" }).click();
  await gate(page, "admin person");
  await page.getByRole("button", { name: "Turn their account off" }).click();
  await expect(page.getByText(/Disabled\./)).toBeVisible();
  await gate(page, "admin person disabled");
  await page.getByRole("button", { name: "Turn their account on" }).click();
  await expect(page.getByText(/Turned on again/)).toBeVisible();
});
