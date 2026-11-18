import type { JsonObject, JsonValue } from "../src/lib/wireValues";
import { isNumber, isJsonObject, isString } from "../src/lib/wireValues";
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

export function recordOf(value: JsonValue | undefined): JsonObject {
  if (!isJsonObject(value)) throw new Error("expected a JSON object");

  return value;
}

export function at(value: JsonValue | undefined, ...path: string[]): JsonValue | undefined {
  let node: JsonValue | undefined = value;

  for (const key of path) node = recordOf(node)[key];

  return node;
}

export function arrayAt(value: JsonValue | undefined, ...path: string[]): JsonValue[] {
  const result = at(value, ...path);

  if (!Array.isArray(result)) throw new Error(`expected an array at ${path.join(".")}`);

  return result;
}

export function numberAt(value: JsonValue | undefined, ...path: string[]): number {
  const node = at(value, ...path);

  if (!isNumber(node)) throw new Error(`expected a number at ${path.join(".")}`);

  return node;
}

export function stringAt(value: JsonValue | undefined, ...path: string[]): string {
  const node = at(value, ...path);

  if (!isString(node)) throw new Error(`expected a string at ${path.join(".")}`);

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

  return (method: string, path: string, data?: JsonValue) => request.fetch(`/api/v1${path}`, { method, data, headers: { "X-CSRF-Token": token } });
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

export type Reason = { requestId: number; text: string; login: string };

export type Many = { id: string; subjectEmail: string; former: string; reasons: Reason[] };

// A person with many reasons: `askers` leaders each ask them for their email in
// each of `groups` groups, so `askers * groups` reasons, returned in the order a
// review lists them (ascending request id). A requester may have 10 open requests
// (policy.ContactOpenLimit), so more than 10 groups needs each request withdrawn
// the moment it is made; a withdrawn request keeps its reason, as a settled one
// does. `deleted` soft-deletes the person afterwards (the legacy review's state);
// otherwise they stay live, for the delete panel's review. Two accounts made with
// different `suffix` never share a person or a group.
export async function makeMany(
  request: APIRequestContext,
  suffix: string,
  former: string,
  opts: { askers: number; groups: number; withdraw: boolean; deleted: boolean },
): Promise<Many> {
  const admin = await apiAs(request, ADMIN);
  const login = `mn-subject-${suffix}`;
  const subject = await makePerson(admin, login, former);
  const people: Person[] = [];

  // Named to sort last: the admin users list shows 25 a page by name, and the specs that share this
  // database (teen-consent) look for their user on the first page.
  for (let i = 1; i <= opts.askers; i++) people.push(await makePerson(admin, `mn-ask${i}-${suffix}`, `Zzz Asker ${i} Many ${suffix}`));
  const groupIds: number[] = [];

  for (let g = 1; g <= opts.groups; g++) {
    const { group } = await admin("POST", "/groups", { name: `Many study ${g} ${suffix}`, description: "Many reasons proof", visibility: "public", tags: [] });
    const id = numberAt(group, "id");
    groupIds.push(id);

    for (const p of people) await admin("POST", `/admin/groups/${id}/members`, { userId: p.id, role: "leader" });
    await admin("POST", `/admin/groups/${id}/members`, { userId: subject.id, role: "member" });
  }

  await (await apiAs(request, login))("PUT", "/me", { hideContactFromLeaders: true });
  const reasons: Reason[] = [];

  for (const [i] of people.entries()) {
    const asker = `mn-ask${i + 1}-${suffix}`;
    const as = await apiAs(request, asker);

    for (const [g, groupId] of groupIds.entries()) {
      const text = `Many ${i + 1}-${g + 1} ${suffix}: please share your email so I can organize rides`;
      const res = await as("POST", "/contact-requests", { subjectUserId: subject.id, contextType: "group", contextId: groupId, fields: "email", reason: text });
      const requestId = numberAt(res, "request", "id");

      if (opts.withdraw) await as("POST", `/contact-requests/${requestId}/withdraw`, {});
      reasons.push({ requestId, text, login: asker });
    }
  }

  reasons.sort((a, b) => a.requestId - b.requestId);

  if (opts.deleted) await tombstone(request, subject.id);

  return { id: String(subject.id), subjectEmail: subject.email, former, reasons };
}

// Another admin session decides one reason through the API: one mint, one PUT.
// Only the deleted-accounts review has this route.
export async function decideElsewhere(request: APIRequestContext, id: string, reasonText: string, choice: "keep" | "remove") {
  const admin = await apiAs(request, ADMIN);
  const item = (await pageOf(admin, id)).find((it) => it.reason.includes(reasonText));

  if (!item) throw new Error(`no reason holding ${reasonText}`);
  const minted = await admin("POST", "/admin/operation-attempts", { kind: "legacy_reason_save", subjectUserId: Number(id) });
  await admin("PUT", `/admin/erasure-reasons/legacy/${id}`, {
    attemptId: stringAt(minted, "attemptId"),
    decisions: [{ requestId: item.requestId, reviewHash: item.reviewHash, choice }],
  });
}

// What the server lists for an account now, as this admin reads it.
export async function pageOf(admin: Admin, id: string) {
  const page = await admin("POST", `/admin/erasure-reasons/legacy/${id}/page`, { after: null });

  return arrayAt(page, "items").map((item) => ({ requestId: stringAt(item, "requestId"), reviewHash: stringAt(item, "reviewHash"), reason: stringAt(item, "reason") }));
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
    const unit: JsonValue = await admin("GET", `/admin/erasure-reasons/legacy${after === null ? "" : `?after=${after}`}`);

    for (const account of arrayAt(unit, "accounts")) {
      const id = stringAt(account, "userId");

      if ((await pageOf(admin, id)).some((item) => item.reason.includes(` ${UI_SUFFIX}:`))) return id;
    }

    const next = at(unit, "next");
    after = isString(next) ? next : null;
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
