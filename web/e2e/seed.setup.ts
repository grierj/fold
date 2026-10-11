import { expect, test as setup } from "@playwright/test";
import { apiAs, email, PASSWORD } from "./helpers";

// Runs once per `make e2e`, on the fresh database e2e/server.sh made.
// Members-only circle (private) and Invite-only circle (Sam is a member).
// Room 201 class (limit 3): Grier (admin, leader), Lee (leader), Sam
// (member). Pat and Quinn were disabled while Sam took the last seat, so
// re-enabling them left both waiting to return (DEC-38, DEC-39).
setup("first-run setup and seed data", async ({ page, request }) => {
  await page.goto("/setup");
  await page.locator("[name=site_name]").fill("Fold e2e");
  await page.locator("[name=timezone]").fill("America/Boise");
  await page.locator("[name=name]").fill("Grier");
  await page.locator("[name=email]").fill(email("grier"));
  await page.locator("[name=password]").fill(PASSWORD);
  await page.locator("[name=password_confirm]").fill(PASSWORD);
  await page.getByRole("button").click();
  await expect(page).not.toHaveURL(/\/setup/);

  const admin = await apiAs(request, "grier");
  const ids: Record<string, number> = {};
  for (const [name, full] of [["lee", "Lee Leader"], ["sam", "Sam Member"], ["pat", "Pat Returner"], ["quinn", "Quinn Returner"]]) {
    const { user } = await admin("POST", "/admin/users", { email: email(name), name: full, password: PASSWORD, role: "member" });
    ids[name] = user.id;
  }
  const { group } = await admin("POST", "/groups", {
    name: "Room 201 class", description: "A class in a small room.", tags: ["class"],
    imageUrl: "", location: "Room 201", visibility: "public", memberLimit: null,
  });
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: ids.lee, role: "leader" });
  for (const name of ["pat", "quinn"]) {
    await admin("POST", `/admin/groups/${group.id}/members`, { userId: ids[name], role: "member" });
    await admin("PUT", `/admin/users/${ids[name]}`, { status: "disabled" });
  }
  await admin("POST", `/admin/groups/${group.id}/members`, { userId: ids.sam, role: "member" });
  await admin("PUT", `/groups/${group.id}`, { memberLimit: 3 });
  for (const name of ["pat", "quinn"]) await admin("PUT", `/admin/users/${ids[name]}`, { status: "active" });

  // Groups only a signed-in member may see (session.spec.ts checks logout
  // leaves none of them behind).
  for (const [name, visibility] of [["Members-only circle", "private"], ["Invite-only circle", "invite_only"]]) {
    const { group: g } = await admin("POST", "/groups", {
      name, description: "Not for visitors.", tags: [], imageUrl: "", location: "", visibility, memberLimit: null,
    });
    await admin("POST", `/admin/groups/${g.id}/members`, { userId: ids.sam, role: "member" });
  }
});
