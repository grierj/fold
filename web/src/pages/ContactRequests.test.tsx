import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { Contacts } from "../contact-api";
import type { ContactGrant, ContactRequest } from "../types";
import { ContactRequestsPage } from "./ContactRequests";

const actor = vi.hoisted(() => ({ user: { id: 2 } as { id: number } | null }));
vi.mock("../auth", () => ({ useAuth: () => actor }));
vi.mock("../contact-api", () => ({ Contacts: { request: vi.fn(), requests: vi.fn(), grants: vi.fn(), decide: vi.fn(), withdraw: vi.fn(), revoke: vi.fn(), reveal: vi.fn() } }));
const request: ContactRequest = { id: 7, requesterId: 1, requesterName: "Lee", subjectUserId: 2, subjectName: "Pat", contextType: "event", contextId: 3, fields: "email", reason: "A private reason", needsAdmin: true, subjectDecision: "pending", adminDecision: "pending", status: "pending", createdAt: "2026-10-01T12:00:00Z", expiresAt: "2026-10-15T12:00:00Z", endReason: "", canSubjectDecide: true, canAdminDecide: false, canWithdraw: false };
const grant: ContactGrant = { id: 8, requestId: 7, requesterId: 1, requesterName: "Lee", subjectUserId: 2, subjectName: "Pat", contextType: "event", fields: "email", needsAdmin: true, grantedAt: "2026-10-02T12:00:00Z", endReason: "", canReveal: false, canRevoke: true };
function mount(path = "/contact-requests/7") {
  return render(<MemoryRouter initialEntries={[path]}><Routes><Route path="/contact-requests" element={<ContactRequestsPage />} /><Route path="/contact-requests/:id" element={<ContactRequestsPage />} /><Route path="/login" element={<p>Sign in first</p>} /></Routes></MemoryRouter>);
}
beforeEach(() => {
  vi.clearAllMocks(); actor.user = { id: 2 };
  vi.mocked(Contacts.request).mockResolvedValue({ request });
  vi.mocked(Contacts.requests).mockResolvedValue({ requests: [] });
  vi.mocked(Contacts.grants).mockResolvedValue({ grants: [] });
  vi.spyOn(window, "confirm").mockReturnValue(true);
});

it.each(["subject", "admin"] as const)("shows independent teen/admin decisions when %s approves first", async (first) => {
  let current = { ...request, canSubjectDecide: first === "subject", canAdminDecide: first === "admin" };
  vi.mocked(Contacts.request).mockImplementation(async () => ({ request: current }));
  vi.mocked(Contacts.decide).mockImplementation(async (_id, party, decision) => {
    current = { ...current, [party === "subject" ? "subjectDecision" : "adminDecision"]: decision, canSubjectDecide: false, canAdminDecide: false };
    return { request: current };
  });
  mount();
  expect(await screen.findByRole("heading", { name: "Teen's decision: Waiting" })).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "Admin's decision: Waiting" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Approve sharing" }));
  expect(Contacts.decide).toHaveBeenCalledWith(7, first, "approved");
  expect(await screen.findByRole("heading", { name: `${first === "subject" ? "Teen's" : "Admin's"} decision: Approved` })).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: `${first === "subject" ? "Admin's" : "Teen's"} decision: Waiting` })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Reveal contact details" })).not.toBeInTheDocument();
  expect(Contacts.reveal).not.toHaveBeenCalled();
});

it("a denial is terminal even if a stale response claims a decision capability", async () => {
  vi.mocked(Contacts.decide).mockResolvedValue({ request });
  vi.mocked(Contacts.request).mockResolvedValueOnce({ request }).mockResolvedValue({ request: { ...request, status: "denied", subjectDecision: "denied", canAdminDecide: true } });
  mount();
  await userEvent.click(await screen.findByRole("button", { name: "Deny request" }));
  expect(await screen.findByRole("heading", { name: "Teen's decision: Denied" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Approve sharing" })).not.toBeInTheDocument();
});

it("guardian notification 404 resolves only through matching authorized active grants, with exclusive pagination", async () => {
  vi.mocked(Contacts.request).mockRejectedValue(new ApiError(404, "PRIVATE REASON"));
  const unrelated = Array.from({ length: 100 }, (_, i) => ({ ...grant, id: 200 - i, requestId: 90 }));
  vi.mocked(Contacts.grants).mockResolvedValueOnce({ grants: unrelated }).mockResolvedValueOnce({ grants: [grant] }).mockResolvedValue({ grants: [] });
  vi.mocked(Contacts.revoke).mockResolvedValue({});
  mount();
  expect(await screen.findByRole("button", { name: "Revoke access" })).toBeInTheDocument();
  expect(Contacts.grants).toHaveBeenNthCalledWith(2, 101);
  expect(screen.queryByText("A private reason")).not.toBeInTheDocument();
  expect(screen.queryByText("PRIVATE REASON")).not.toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: /decision/ })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Approve sharing" })).not.toBeInTheDocument();
  expect(screen.queryByRole("link", { name: "View event" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Revoke access" }));
  expect(Contacts.revoke).toHaveBeenCalledWith(8);
  expect(await screen.findByText("Contact access is not available")).toBeInTheDocument();
});

it("does not recover ended grants or another request's grants for a guardian", async () => {
  vi.mocked(Contacts.request).mockRejectedValue(new ApiError(404, "private"));
  vi.mocked(Contacts.grants).mockResolvedValue({ grants: [{ ...grant, endedAt: "2026-10-03T12:00:00Z" }, { ...grant, id: 9, requestId: 99 }] });
  mount();
  expect(await screen.findByText("Contact access is not available")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Revoke access" })).not.toBeInTheDocument();
});

it("withdraws only when capable and preserves the review on friendly server failure", async () => {
  vi.mocked(Contacts.request).mockResolvedValue({ request: { ...request, canSubjectDecide: false, canWithdraw: true } });
  vi.mocked(Contacts.withdraw).mockRejectedValue(new ApiError(403, "SECRET", { code: "contact_unavailable" }));
  mount();
  await userEvent.click(await screen.findByRole("button", { name: "Withdraw request" }));
  expect(Contacts.withdraw).toHaveBeenCalledWith(7);
  expect(await screen.findByRole("alert")).toHaveTextContent("no longer available to you");
  expect(screen.getByText("A private reason")).toBeInTheDocument();
});

it("paginates request and grant histories using their own last exclusive IDs", async () => {
  vi.mocked(Contacts.requests).mockResolvedValueOnce({ requests: Array.from({ length: 100 }, (_, i) => ({ ...request, id: 300 - i })) }).mockResolvedValue({ requests: [{ ...request, id: 200 }] });
  vi.mocked(Contacts.grants).mockResolvedValueOnce({ grants: Array.from({ length: 100 }, (_, i) => ({ ...grant, id: 500 - i, canRevoke: false })) }).mockResolvedValue({ grants: [] });
  mount("/contact-requests");
  await userEvent.click(await screen.findByRole("button", { name: "Load older requests" }));
  expect(Contacts.requests).toHaveBeenLastCalledWith(201);
  await userEvent.click(screen.getByRole("button", { name: "Load older grants" }));
  expect(Contacts.grants).toHaveBeenLastCalledWith(401);
  expect(within(screen.getByRole("region", { name: "Request history" })).getAllByRole("listitem")).toHaveLength(101);
  expect(Contacts.reveal).not.toHaveBeenCalled();
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => { resolve = resolvePromise; reject = rejectPromise; });
  return { promise, resolve, reject };
}

for (const kind of ["requests", "grants"] as const) {
  function historyPages() {
    const page = vi.fn<(before?: number) => Promise<number[]>>();
    if (kind === "requests") vi.mocked(Contacts.requests).mockImplementation(async (before) => ({ requests: (await page(before)).map((id) => ({ ...request, id, subjectName: `${kind} person ${id}` })) }));
    else vi.mocked(Contacts.grants).mockImplementation(async (before) => ({ grants: (await page(before)).map((id) => ({ ...grant, id, subjectName: `${kind} person ${id}`, canRevoke: false })) }));
    return page;
  }
  const firstIds = Array.from({ length: 100 }, (_, i) => 300 - i);
  const refreshedIds = Array.from({ length: 100 }, (_, i) => 301 - i);
  const olderButton = () => screen.getByRole("button", { name: `Load older ${kind}` });
  const row = (id: number) => screen.queryByText(`Lee → ${kind} person ${id}`);

  it.each(["button", "focus"])(`discards stale ${kind} pagination after %s refresh and keeps the refreshed cursor`, async (trigger) => {
    const old = deferred<number[]>();
    const page = historyPages().mockResolvedValueOnce(firstIds).mockReturnValueOnce(old.promise).mockResolvedValueOnce(refreshedIds).mockResolvedValueOnce([201]);
    mount("/contact-requests");
    await userEvent.click(await screen.findByRole("button", { name: `Load older ${kind}` }));
    expect(page).toHaveBeenLastCalledWith(201);
    if (trigger === "button") await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    else fireEvent.focus(window);
    expect(await screen.findByText(`Lee → ${kind} person 301`)).toBeInTheDocument();
    await act(async () => { old.resolve([200, 199]); });
    expect(row(200)).not.toBeInTheDocument();
    expect(olderButton()).toBeEnabled();
    await userEvent.click(olderButton());
    expect(page).toHaveBeenLastCalledWith(202);
    expect(await screen.findByText(`Lee → ${kind} person 201`)).toBeInTheDocument();
  });

  it.each(["success", "error"])(`ignores stale ${kind} pagination %s/completion while current pagination is pending`, async (outcome) => {
    const old = deferred<number[]>();
    const current = deferred<number[]>();
    historyPages().mockResolvedValueOnce(firstIds).mockReturnValueOnce(old.promise).mockResolvedValueOnce(refreshedIds).mockReturnValueOnce(current.promise);
    mount("/contact-requests");
    await userEvent.click(await screen.findByRole("button", { name: `Load older ${kind}` }));
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await userEvent.click(await screen.findByRole("button", { name: `Load older ${kind}` }));
    expect(olderButton()).toBeDisabled();
    await act(async () => {
      if (outcome === "success") old.resolve([200]);
      else old.reject(new ApiError(403, "private", { code: "contact_unavailable" }));
    });
    expect(row(200)).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(olderButton()).toBeDisabled();
    await act(async () => { current.resolve([201]); });
    expect(row(201)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: `Load older ${kind}` })).not.toBeInTheDocument();
  });

  it(`discards superseded ${kind} first loads`, async () => {
    const old = deferred<number[]>();
    historyPages().mockReturnValueOnce(old.promise).mockResolvedValueOnce(refreshedIds);
    mount("/contact-requests");
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText(`Lee → ${kind} person 301`)).toBeInTheDocument();
    await act(async () => { old.resolve(firstIds); });
    expect(row(301)).toBeInTheDocument();
    expect(row(201)).not.toBeInTheDocument();
  });

  it.each(["first load", "pagination"])(`discards ${kind} %s results after unmount`, async (stage) => {
    const old = deferred<number[]>();
    const page = historyPages();
    if (stage === "pagination") page.mockResolvedValueOnce(firstIds);
    page.mockReturnValueOnce(old.promise).mockResolvedValueOnce(refreshedIds);
    const view = mount("/contact-requests");
    if (stage === "pagination") await userEvent.click(await screen.findByRole("button", { name: `Load older ${kind}` }));
    view.unmount();
    mount("/contact-requests");
    expect(await screen.findByText(`Lee → ${kind} person 301`)).toBeInTheDocument();
    await act(async () => { old.resolve([200]); });
    expect(row(301)).toBeInTheDocument();
    expect(row(200)).not.toBeInTheDocument();
    expect(olderButton()).toBeEnabled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
}

it("redirects signed-out visitors without fetching requests or grants", async () => {
  actor.user = null; mount();
  expect(await screen.findByText("Sign in first")).toBeInTheDocument();
  expect(Contacts.request).not.toHaveBeenCalled(); expect(Contacts.grants).not.toHaveBeenCalled();
});
