import { expect, type APIRequestContext } from "@playwright/test";
import { apiAs, email, PASSWORD } from "./helpers";

// Deleted accounts with undecided reasons, built through the real API as the
// people would (a leader asks a member for contact details, with a reason) and
// then soft-deleted through the test binary's one-row seed route
// (attempt_clock_e2e.go, built only with -tags e2eclock). No live path leaves
// that state: a deletion decides every reason it reaches. Shared by
// admin-reason-review.spec.ts and the page checks in ui-pages.ts.

const ADMIN = "grier";

export type Admin = Awaited<ReturnType<typeof apiAs>>;
export type Person = { id: number; email: string; name: string };
export type Fixture = { id: string; texts: string[]; requestIds: number[]; former: string; subjectEmail: string };

export function recordOf(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error("expected a JSON object");
  return Object.fromEntries(Object.entries(value));
}

export function at(value: unknown, ...path: string[]): unknown {
  let node: unknown = value;
  for (const key of path) node = recordOf(node)[key];
  return node;
}

export function numberAt(value: unknown, ...path: string[]): number {
  const node = at(value, ...path);
  if (typeof node !== "number") throw new Error(`expected a number at ${path.join(".")}`);
  return node;
}

export function stringAt(value: unknown, ...path: string[]): string {
  const node = at(value, ...path);
  if (typeof node !== "string") throw new Error(`expected a string at ${path.join(".")}`);
  return node;
}

export async function makePerson(admin: Admin, login: string, name: string, role = "member"): Promise<Person> {
  const made = await admin("POST", "/admin/users", { ageBand: "adult", name, email: email(login), password: PASSWORD, role });
  return { id: numberAt(made, "user", "id"), email: email(login), name };
}

// A signed-in admin whose answers are not asserted ok, for the refusals.
export async function rawAdmin(request: APIRequestContext) {
  await apiAs(request, ADMIN);
  const token = stringAt(await (await request.get("/api/v1/auth/csrf")).json(), "csrfToken");
  return (method: string, path: string, data?: unknown) => request.fetch(`/api/v1${path}`, { method, data, headers: { "X-CSRF-Token": token } });
}

// Soft-deletes one live account with its reasons undecided; 200 only when exactly
// that account became a tombstone.
export async function tombstone(request: APIRequestContext, userId: number | string) {
  const admin = await rawAdmin(request);
  const res = await admin("POST", "/e2e/legacy-tombstone", { userId: Number(userId) });
  expect(res.status(), await res.text()).toBe(200);
  expect(numberAt(await res.json(), "userId")).toBe(Number(userId));
}

// A person named `former` who asked nothing but was asked for their email by
// `reasons` group leaders, each with a reason of their own, and was then
// deleted. The member hides their contact details from leaders, which is what
// makes a leader ask. Requests are listed in ascending id, so reason n is
// asker n's. The reasons never contain the former name.
export async function makeLegacy(request: APIRequestContext, suffix: string, former: string, reasons = 2): Promise<Fixture> {
  const admin = await apiAs(request, ADMIN);
  const login = `lg-subject-${suffix}`;
  const subject = await makePerson(admin, login, former);
  const keeper = await makePerson(admin, `lg-keeper-${suffix}`, `Kit Keeper ${suffix}`);
  const askers: Person[] = [];
  for (let i = 1; i <= reasons; i++) askers.push(await makePerson(admin, `lg-ask${i}-${suffix}`, `Lia ${i} Asker ${suffix}`));
  const { group } = await admin("POST", "/groups", { name: `Legacy study ${suffix}`, description: "Legacy review proof", visibility: "public", tags: [] });
  const groupId = numberAt(group, "id");
  for (const p of [keeper, ...askers]) await admin("POST", `/admin/groups/${groupId}/members`, { userId: p.id, role: "leader" });
  await admin("POST", `/admin/groups/${groupId}/members`, { userId: subject.id, role: "member" });
  await (await apiAs(request, login))("PUT", "/me", { hideContactFromLeaders: true });
  const texts: string[] = [];
  const requestIds: number[] = [];
  for (const [i] of askers.entries()) {
    const text = `Reason ${i + 1} ${suffix}: please share your email so I can organize rides`;
    const asker = await apiAs(request, `lg-ask${i + 1}-${suffix}`);
    const res = await asker("POST", "/contact-requests", {
      subjectUserId: subject.id, contextType: "group", contextId: groupId, fields: "email", reason: text,
    });
    const id = numberAt(res, "request", "id");
    // Withdrawn, so the request is settled and keeps its reason, as the ones a
    // legacy deletion left are. A request still open when its subject goes is
    // ended by the next review read, which would change its hash mid-test.
    await asker("POST", `/contact-requests/${id}/withdraw`, {});
    texts.push(text);
    requestIds.push(id);
  }
  await tombstone(request, subject.id);
  return { id: String(subject.id), texts, requestIds, former, subjectEmail: subject.email };
}

// What the server lists for an account now, as this admin reads it.
export async function pageOf(admin: Admin, id: string) {
  const page = await admin("POST", `/admin/erasure-reasons/legacy/${id}/page`, { after: null });
  return (at(page, "items") as unknown[]).map((item) => ({ requestId: stringAt(item, "requestId"), reviewHash: stringAt(item, "reviewHash"), reason: stringAt(item, "reason") }));
}

// The deleted account the page checks (a11y.spec.ts, layout.spec.ts) open: found
// by the reasons it was made with (a suffix no spec's account shares), made when
// the database has none yet. If another run of the page checks is making it at the
// same moment, making it fails on the shared emails, and this waits for theirs.
const UI_SUFFIX = "uicheck";

async function findUILegacyAccount(request: APIRequestContext): Promise<string | null> {
  const admin = await apiAs(request, ADMIN);
  let after: string | null = null;
  do {
    const unit: unknown = await admin("GET", `/admin/erasure-reasons/legacy${after === null ? "" : `?after=${after}`}`);
    for (const account of at(unit, "accounts") as unknown[]) {
      const id = stringAt(account, "userId");
      if ((await pageOf(admin, id)).some((item) => item.reason.includes(` ${UI_SUFFIX}:`))) return id;
    }
    const next = at(unit, "next");
    after = typeof next === "string" ? next : null;
  } while (after !== null);
  return null;
}

export async function uiLegacyAccount(request: APIRequestContext): Promise<string> {
  const found = await findUILegacyAccount(request);
  if (found !== null) return found;
  try {
    return (await makeLegacy(request, UI_SUFFIX, "Una Former", 3)).id;
  } catch (e) {
    const until = Date.now() + 30_000;
    for (;;) {
      const id = await findUILegacyAccount(request);
      if (id !== null) return id;
      if (Date.now() > until) throw e;
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
  }
}
