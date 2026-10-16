import { createRequire } from "node:module";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

const require = createRequire(new URL("../../../web/package.json", import.meta.url));
const { chromium } = require("playwright");

const url = process.env.VERIFY_URL;
const evidence = process.env.VERIFY_EVIDENCE;
if (!url || !evidence) {
  console.error("drive-sign-in: set VERIFY_URL and VERIFY_EVIDENCE");
  process.exit(1);
}

const email = "verify-admin@example.com";
const password = "password1234";
const name = "Verify Admin";
const dir = path.join(evidence, "sign-in");
await mkdir(dir, { recursive: true });

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
page.setDefaultTimeout(15_000);
try {
  await page.goto(url + "/setup");
  await page.locator("[name=site_name]").fill("Fold verify");
  await page.locator("[name=timezone]").fill("America/Boise");
  await page.locator("[name=name]").fill(name);
  await page.locator("[name=email]").fill(email);
  await page.locator("[name=password]").fill(password);
  await page.locator("[name=password_confirm]").fill(password);
  await page.getByRole("button", { name: "Create site" }).click();
  await page.waitForURL((u) => u.pathname === "/login");
  await page.getByRole("heading", { name: "Log in to Fold" }).waitFor();

  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill("not-the-password");
  await page.getByRole("button", { name: "Log in" }).click();
  await page.getByText("That email and password don't match. Check them and try again.").waitFor();
  if (!page.url().includes("/login")) {
    throw new Error("a bad password left the login page: " + page.url());
  }
  await page.screenshot({ path: path.join(dir, "bad-password.png"), fullPage: true });

  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Log in" }).click();
  await page.waitForURL((u) => !u.pathname.startsWith("/login"));
  await page.getByRole("heading", { name: "Find your people. Grow in faith together." }).waitFor();
  await page.getByRole("button", { name: "Log out" }).waitFor();
  await page.locator("header").getByText(name, { exact: true }).waitFor();
  await page.screenshot({ path: path.join(dir, "after-login.png"), fullPage: true });

  const me = await page.request.get(url + "/api/v1/auth/me");
  const meBody = await me.text();
  await writeFile(path.join(dir, "me.json"), meBody);
  if (me.status() !== 200) {
    throw new Error("GET /api/v1/auth/me returned " + me.status() + " " + meBody);
  }
  const meJson = JSON.parse(meBody);
  if (meJson.user?.email !== email || meJson.user?.name !== name) {
    throw new Error("auth/me user was " + meBody);
  }

  await page.reload();
  await page.getByRole("heading", { name: "Find your people. Grow in faith together." }).waitFor();
  await page.getByRole("button", { name: "Log out" }).waitFor();
  await page.locator("header").getByText(name, { exact: true }).waitFor();
  await page.screenshot({ path: path.join(dir, "after-reload.png"), fullPage: true });

  await writeFile(
    path.join(dir, "result.txt"),
    [
      "url " + page.url(),
      "heading Find your people. Grow in faith together.",
      "header " + name,
      "me 200 " + email,
    ].join("\n") + "\n"
  );
} finally {
  await browser.close();
}
