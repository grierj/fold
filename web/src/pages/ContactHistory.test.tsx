import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ApiError, Site } from "../api";
import { ContactHistory } from "../contact-history-api";
import type { ContactHistoryEntry, ContactHistoryResponse } from "../types";
import { ContactHistoryPage } from "./ContactHistory";

const auth = vi.hoisted(() => ({ user: { id: 1, role: "admin", status: "active" } as { id: number; role: string; status: string } | null, loading: false, isAdmin: true }));
vi.mock("../auth", () => ({ useAuth: () => auth }));
vi.mock("../api", async (actual) => ({ ...await actual<typeof import("../api")>(), Site: { get: vi.fn() } }));
vi.mock("../contact-history-api", async (actual) => ({ ...await actual<typeof import("../contact-history-api")>(), ContactHistory: { people: vi.fn(), list: vi.fn(), csv: vi.fn() } }));
const entry: ContactHistoryEntry = { id: "9007199254740993", at: "2026-10-01T12:00:00Z", action: "contact.decision", outcome: "decision", actorId: "9223372036854775807", requesterId: "9007199254740992", subjectUserId: "9007199254740993", purpose: "contact", requestId: "9223372036854775807", contextType: "group", contextId: "9007199254740993", party: "subject", decision: "approved", fields: "both" };
const empty = { purpose: "", action: "", from: "", until: "" };
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (reason: unknown) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }
function Switch() { const navigate = useNavigate(); return <button onClick={() => navigate("/admin/users/9007199254740992/contact-history")}>Other person</button>; }
const openFilters = () => userEvent.click(screen.getByRole("button", { name: "Filters" }));
function mount(path = "/admin/users/9223372036854775807/contact-history") {
  return render(<MemoryRouter initialEntries={[path]}><Switch /><Routes>
    <Route path="/admin/users/:id/contact-history" element={<ContactHistoryPage />} />
    <Route path="/admin/contact-history" element={<ContactHistoryPage />} />
    <Route path="/login" element={<p>Sign in first</p>} />
  </Routes></MemoryRouter>);
}
beforeEach(() => {
  vi.clearAllMocks(); auth.user = { id: 1, role: "admin", status: "active" }; auth.isAdmin = true;
  vi.mocked(ContactHistory.people).mockResolvedValue({ people: [] });
  vi.mocked(Site.get).mockResolvedValue({ name: "Church", timezone: "America/Denver", selfSignup: "off", roomsEnabled: false, pushEnabled: true });
  vi.mocked(ContactHistory.list).mockResolvedValue({ entries: [entry], nextBefore: "9007199254740993" });
  vi.mocked(ContactHistory.csv).mockResolvedValue(new Blob(["auditId,at\r\n"]));
  vi.stubGlobal("URL", class extends URL {
    static createObjectURL = vi.fn(() => "blob:history");
    static revokeObjectURL = vi.fn();
  });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it("renders controlled decisions/provenance with exact distinct IDs, never invented context links or contact values", async () => {
  mount();
  expect(await screen.findByText("Person's decision: Approved")).toBeInTheDocument();
  expect(screen.getByText(/Requester #9007199254740992 · Subject #9007199254740993/)).toBeInTheDocument();
  expect(screen.getByText(/Audit #9007199254740993 · Actor #9223372036854775807/)).toBeInTheDocument();
  expect(screen.getByText(/Email and phone selected/)).toBeInTheDocument();
  expect(screen.getAllByRole("link")).toHaveLength(1);
  expect(screen.getByRole("link")).toHaveAttribute("href", "/admin?tab=users");
});
it("uses returned cursors unchanged, keeps server order and suppresses exact duplicates only", async () => {
  vi.mocked(ContactHistory.list).mockResolvedValueOnce({ entries: [entry], nextBefore: "9007199254740993" }).mockResolvedValue({ entries: [entry, { ...entry, id: "9007199254740992" }] });
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Load older entries" }));
  expect(ContactHistory.list).toHaveBeenLastCalledWith("9223372036854775807", empty, "9007199254740993");
  const rows = screen.getAllByRole("listitem"); expect(rows).toHaveLength(2);
  expect(rows[0]).toHaveTextContent("Audit #9007199254740993"); expect(rows[1]).toHaveTextContent("Audit #9007199254740992");
});
it("separates drafts from submitted/export filters and preserves a valid date draft on server failure", async () => {
  mount(); await screen.findByText("Person's decision: Approved");
  await openFilters();
  fireEvent.change(screen.getByLabelText("From (optional)"), { target: { value: "2026-10-01" } });
  await userEvent.click(screen.getByRole("button", { name: "Export all matching CSV" }));
  expect(ContactHistory.csv).toHaveBeenLastCalledWith("9223372036854775807", empty);
  vi.mocked(ContactHistory.list).mockRejectedValueOnce(new ApiError(400, "SECRET", { code: "contact_history_filters" }));
  await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Check the dates and filters");
  expect(screen.getByLabelText("From (optional)")).toHaveValue("2026-10-01");
  expect(screen.queryByText("SECRET")).not.toBeInTheDocument();
  expect(screen.queryByText("Person's decision: Approved")).not.toBeInTheDocument();
});
it("supports manual deleted IDs and neutral empty history without a live user lookup", async () => {
  vi.mocked(ContactHistory.list).mockResolvedValue({ entries: [] });
  mount("/admin/contact-history");
  expect(screen.getByText("Choose a person")).toBeInTheDocument();
  await userEvent.click(screen.getByText("Look up a deleted account"));
  await userEvent.type(screen.getByLabelText("Saved person ID"), "9223372036854775807");
  await userEvent.click(screen.getByRole("button", { name: "Show history" }));
  expect(await screen.findByText("No matching history")).toBeInTheDocument();
  expect(ContactHistory.list).toHaveBeenCalledWith("9223372036854775807", empty);
  expect(screen.getByRole("button", { name: "Export all matching CSV" })).toBeEnabled();
});
it("rejects overflow without coercion and preserves all draft fields", async () => {
  mount("/admin/contact-history");
  await userEvent.click(screen.getByText("Look up a deleted account"));
  await userEvent.type(screen.getByLabelText("Saved person ID"), "9223372036854775808");
  await openFilters();
  await userEvent.click(screen.getByLabelText("Direct contact"));
  fireEvent.change(screen.getByLabelText("Until (optional)"), { target: { value: "2026-11-01" } });
  await userEvent.click(screen.getByRole("button", { name: "Show history" }));
  expect(screen.getByText("Check the saved person ID. Use the digits saved before the account was deleted.")).toBeInTheDocument();
  expect(screen.getByLabelText("Saved person ID")).toHaveValue("9223372036854775808");
  await waitFor(() => expect(screen.getByLabelText("Saved person ID")).toHaveFocus());
  expect(screen.getByLabelText("Until (optional)")).toHaveValue("2026-11-01");
  expect(screen.getByLabelText("Direct contact")).toBeChecked(); expect(ContactHistory.list).not.toHaveBeenCalled();
});
it("finds a person by friendly metadata and submits the exact returned string ID", async () => {
  vi.mocked(ContactHistory.people).mockImplementation(async ({ q }) => ({
    people: q === "Pat" ? [{ id: "9007199254740993", name: "Pat Friendly", email: "pat@example.com" }] : [],
  }));
  mount("/admin/contact-history");
  await userEvent.type(screen.getByLabelText("Find a person"), "Pat");
  await screen.findByRole("option", { name: "Pat Friendly — pat@example.com" });
  expect(screen.queryByRole("heading", { name: /^History for/ })).not.toBeInTheDocument();
  await userEvent.selectOptions(screen.getByLabelText("Person"), "9007199254740993");
  // No separate Apply step for the person.
  expect(await screen.findByRole("heading", { name: "History for Pat Friendly" })).toBeInTheDocument();
  expect(ContactHistory.list).toHaveBeenLastCalledWith("9007199254740993", empty);
});

it("converts inclusive church dates to UTC once and exports the applied snapshot", async () => {
  mount(); await screen.findByText("Person's decision: Approved");
  await openFilters();
  fireEvent.change(screen.getByLabelText("From (optional)"), { target: { value: "2026-03-08" } });
  fireEvent.change(screen.getByLabelText("Until (optional)"), { target: { value: "2026-03-08" } });
  await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  const applied = { purpose: "", action: "", from: "2026-03-08T07:00:00.000Z", until: "2026-03-09T06:00:00.000Z" };
  await waitFor(() => expect(ContactHistory.list).toHaveBeenLastCalledWith("9223372036854775807", applied));
  expect(screen.getByText(/From March 8, 2026 · Through March 8, 2026/)).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText("Until (optional)"), { target: { value: "2026-03-09" } });
  await userEvent.click(screen.getByRole("button", { name: "Export all matching CSV" }));
  expect(ContactHistory.csv).toHaveBeenLastCalledWith("9223372036854775807", applied);
});

it("disables date application until retry loads the church timezone and never falls back to browser UTC", async () => {
  vi.mocked(Site.get).mockRejectedValueOnce(new TypeError("offline")).mockResolvedValueOnce({ name: "Church", timezone: "America/Denver", selfSignup: "off", roomsEnabled: false, pushEnabled: true });
  mount(); await screen.findByText("Person's decision: Approved");
  await openFilters();
  fireEvent.change(screen.getByLabelText("From (optional)"), { target: { value: "2026-11-01" } });
  fireEvent.change(screen.getByLabelText("Until (optional)"), { target: { value: "2026-11-01" } });
  expect(await screen.findByRole("button", { name: "Apply filters" })).toBeDisabled();
  expect(ContactHistory.list).toHaveBeenCalledTimes(1);
  await userEvent.click(await screen.findByRole("button", { name: "Try loading church time again" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Apply filters" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() => expect(ContactHistory.list).toHaveBeenLastCalledWith("9223372036854775807", {
    purpose: "", action: "", from: "2026-11-01T06:00:00.000Z", until: "2026-11-02T07:00:00.000Z",
  }));
});

it.each([401, 403])("clears history when person metadata lookup loses authority %s", async (status) => {
  vi.mocked(ContactHistory.people).mockRejectedValue(new ApiError(status, "SECRET"));
  mount();
  if (status === 401) expect(await screen.findByText("Sign in first")).toBeInTheDocument();
  else expect(await screen.findByText(/This history is no longer available/)).toBeInTheDocument();
  expect(screen.queryByText("Person's decision: Approved")).not.toBeInTheDocument();
  expect(screen.queryByText("SECRET")).not.toBeInTheDocument();
});

it.each([401, 403])("clears all history and suppresses pending CSV on authority loss %s", async (status) => {
  const pending = deferred<Blob>(); vi.mocked(ContactHistory.csv).mockReturnValue(pending.promise);
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Export all matching CSV" }));
  vi.mocked(ContactHistory.list).mockRejectedValue(new ApiError(status, "SECRET"));
  await userEvent.click(screen.getByRole("button", { name: "Load older entries" }));
  await screen.findByText(status === 401 ? "Sign in first" : /This history is no longer available/);
  await act(async () => pending.resolve(new Blob(["private"])));
  expect(screen.queryByText("Person's decision: Approved")).not.toBeInTheDocument();
  expect(URL.createObjectURL).not.toHaveBeenCalled();
});
it("signed-out and nonadmin pages never fetch protected history", () => {
  auth.user = null; const view = mount(); expect(screen.getByText("Sign in first")).toBeInTheDocument(); view.unmount();
  auth.user = { id: 1, role: "member", status: "active" }; auth.isAdmin = false; mount();
  expect(screen.getByText("This page is for church admins only.")).toBeInTheDocument(); expect(ContactHistory.list).not.toHaveBeenCalled(); expect(ContactHistory.csv).not.toHaveBeenCalled();
});
it.each(["focus", "filter", "person", "unmount"])("ignores late page and CSV responses after %s", async (change) => {
  const page = deferred<ContactHistoryResponse>(); const csv = deferred<Blob>();
  vi.mocked(ContactHistory.csv).mockReturnValue(csv.promise);
  const view = mount(); await screen.findByText("Person's decision: Approved");
  vi.mocked(ContactHistory.list).mockReturnValueOnce(page.promise).mockResolvedValue({ entries: [] });
  await userEvent.click(screen.getByRole("button", { name: "Export all matching CSV" }));
  await userEvent.click(screen.getByRole("button", { name: "Load older entries" }));
  if (change === "focus") fireEvent(window, new Event("focus"));
  if (change === "filter") { await openFilters(); await userEvent.click(screen.getByRole("button", { name: "Apply filters" })); }
  if (change === "person") await userEvent.click(screen.getByRole("button", { name: "Other person" }));
  if (change === "unmount") view.unmount();
  expect(screen.queryByText("Person's decision: Approved")).not.toBeInTheDocument();
  await act(async () => { page.resolve({ entries: [entry] }); csv.resolve(new Blob(["private"])); });
  expect(screen.queryByText("Person's decision: Approved")).not.toBeInTheDocument(); expect(URL.createObjectURL).not.toHaveBeenCalled();
});
it("does not let an old initial response restore data after focus", async () => {
  const initial = deferred<ContactHistoryResponse>(); vi.mocked(ContactHistory.list).mockReturnValueOnce(initial.promise).mockResolvedValue({ entries: [] });
  mount(); fireEvent(window, new Event("focus")); await screen.findByText("No matching history");
  await act(async () => initial.resolve({ entries: [entry] })); expect(screen.queryByText("Person's decision: Approved")).not.toBeInTheDocument();
});
it("blocks duplicate downloads, derives a safe filename, removes anchor and revokes blobs after use or unmount", async () => {
  const pending = deferred<Blob>(); vi.mocked(ContactHistory.csv).mockReturnValue(pending.promise);
  const view = mount(); await userEvent.click(await screen.findByRole("button", { name: "Export all matching CSV" }));
  expect(screen.getByRole("button", { name: "Preparing CSV…" })).toBeDisabled(); expect(ContactHistory.csv).toHaveBeenCalledOnce();
  let filename = ""; vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) { filename = this.download; });
  await act(async () => pending.resolve(new Blob(["csv"])));
  expect(filename).toBe("contact-history-9223372036854775807.csv"); expect(document.querySelector("a[download]")).toBeNull();
  expect(await screen.findByText(/not confirmed receipt/)).toBeInTheDocument();
  await waitFor(() => expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:history"), { timeout: 2000 });
  await userEvent.click(screen.getByRole("button", { name: "Export all matching CSV" }));
  view.unmount(); expect(URL.revokeObjectURL).toHaveBeenCalledTimes(2);
});
it.each([new ApiError(500, "SECRET"), new TypeError("NETWORK SECRET")])("handles download failure without protected output or stale disclosure", async (error) => {
  vi.mocked(ContactHistory.csv).mockRejectedValue(error); mount();
  await userEvent.click(await screen.findByRole("button", { name: "Export all matching CSV" }));
  expect(await screen.findByRole("alert")).not.toHaveTextContent("SECRET");
  expect(screen.queryByText("Person's decision: Approved")).not.toBeInTheDocument(); expect(URL.createObjectURL).not.toHaveBeenCalled();
});
it("never renders arbitrary action/outcome/party/decision/contact/context text", async () => {
  vi.mocked(ContactHistory.list).mockResolvedValue({ entries: [{ ...entry, action: "SECRET ACTION", outcome: "SECRET OUTCOME", party: "SECRET PARTY", decision: "SECRET DECISION", fields: "secret@example.com", contextType: "SECRET GROUP" }] });
  mount(); expect(await screen.findByText("Recorded activity")).toBeInTheDocument(); expect(screen.queryByText(/SECRET|secret@example/)).not.toBeInTheDocument();
});

it("keeps purpose, action and dates under a Filters disclosure", async () => {
  mount(); await screen.findByText("Person's decision: Approved");
  const toggle = screen.getByRole("button", { name: "Filters" });
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  for (const label of ["Direct contact", "Action (optional)", "From (optional)"]) expect(screen.queryByLabelText(label)).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Apply filters" })).not.toBeInTheDocument();
  await userEvent.click(toggle);
  expect(toggle).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByLabelText("Direct contact")).toBeInTheDocument();
  expect(screen.getByLabelText("Action (optional)")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Apply filters" })).toBeEnabled();
  await userEvent.click(toggle);
  expect(screen.queryByLabelText("From (optional)")).not.toBeInTheDocument();
});

it("shows results as soon as a person is chosen, with the filters as drafted, and keeps the privacy line", async () => {
  vi.mocked(ContactHistory.people).mockResolvedValue({ people: [{ id: "42", name: "Pat Friendly", email: "pat@example.com" }] });
  mount("/admin/contact-history");
  expect(screen.getByText(/History records what happened, not the contact details themselves\./)).toBeInTheDocument();
  expect(screen.queryByText(/saved person ID\./)).not.toBeInTheDocument();
  await openFilters(); await userEvent.click(screen.getByLabelText("Direct contact")); await openFilters();
  await screen.findByRole("option", { name: "Pat Friendly — pat@example.com" });
  await userEvent.selectOptions(screen.getByLabelText("Person"), "42");
  await waitFor(() => expect(ContactHistory.list).toHaveBeenCalledWith("42", { ...empty, purpose: "contact" }));
  expect(await screen.findByText("Person's decision: Approved")).toBeInTheDocument();
});

it("renames the saved-ID lookup and explains who it is for", async () => {
  mount("/admin/contact-history");
  expect(screen.queryByText("Use a saved person ID")).not.toBeInTheDocument();
  await userEvent.click(screen.getByText("Look up a deleted account"));
  expect(screen.getByText(/For someone whose account was deleted; ask whoever has their old ID\./)).toBeInTheDocument();
});

it("counts the applied filters on the collapsed Filters button", async () => {
  mount(); await screen.findByText("Person's decision: Approved");
  expect(screen.getByRole("button", { name: "Filters" })).toBeInTheDocument();
  await openFilters();
  await userEvent.click(screen.getByLabelText("Direct contact"));
  await userEvent.selectOptions(screen.getByLabelText("Action (optional)"), "contact.reveal");
  await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() => expect(ContactHistory.list).toHaveBeenLastCalledWith("9223372036854775807", { ...empty, purpose: "contact", action: "contact.reveal" }));
  const toggle = await screen.findByRole("button", { name: "Filters (2 applied)" });
  expect(toggle).toHaveAttribute("aria-expanded", "true");
  await userEvent.click(toggle);
  expect(screen.queryByRole("button", { name: "Apply filters" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Filters (2 applied)" })).toHaveAttribute("aria-expanded", "false");
});
