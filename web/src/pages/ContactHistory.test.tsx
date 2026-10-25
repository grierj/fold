import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { ContactHistory } from "../contact-history-api";
import type { ContactHistoryEntry, ContactHistoryResponse } from "../types";
import { ContactHistoryPage } from "./ContactHistory";

const auth = vi.hoisted(() => ({ user: { id: 1, role: "admin", status: "active" } as { id: number; role: string; status: string } | null, loading: false, isAdmin: true }));
vi.mock("../auth", () => ({ useAuth: () => auth }));
vi.mock("../contact-history-api", async (actual) => ({ ...await actual<typeof import("../contact-history-api")>(), ContactHistory: { list: vi.fn(), csv: vi.fn() } }));
const entry: ContactHistoryEntry = { id: "9007199254740993", at: "2026-10-01T12:00:00Z", action: "contact.decision", outcome: "decision", actorId: "9223372036854775807", requesterId: "9007199254740992", subjectUserId: "9007199254740993", purpose: "contact", requestId: "9223372036854775807", contextType: "group", contextId: "9007199254740993", party: "subject", decision: "approved", fields: "both" };
const empty = { purpose: "", action: "", from: "", until: "" };
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (reason: unknown) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }
function Switch() { const navigate = useNavigate(); return <button onClick={() => navigate("/admin/users/9007199254740992/contact-history")}>Other person</button>; }
function mount(path = "/admin/users/9223372036854775807/contact-history") {
  return render(<MemoryRouter initialEntries={[path]}><Switch /><Routes>
    <Route path="/admin/users/:id/contact-history" element={<ContactHistoryPage />} />
    <Route path="/admin/contact-history" element={<ContactHistoryPage />} />
    <Route path="/login" element={<p>Sign in first</p>} />
  </Routes></MemoryRouter>);
}
beforeEach(() => {
  vi.clearAllMocks(); auth.user = { id: 1, role: "admin", status: "active" }; auth.isAdmin = true;
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
it("separates drafts from submitted/export filters and preserves invalid dates on server failure", async () => {
  mount(); await screen.findByText("Person's decision: Approved");
  await userEvent.type(screen.getByLabelText("From (optional)"), "bad date");
  await userEvent.click(screen.getByRole("button", { name: "Export all matching CSV" }));
  expect(ContactHistory.csv).toHaveBeenLastCalledWith("9223372036854775807", empty);
  vi.mocked(ContactHistory.list).mockRejectedValueOnce(new ApiError(400, "SECRET", { code: "contact_history_filters" }));
  await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Check the dates and filters");
  expect(screen.getByLabelText("From (optional)")).toHaveValue("bad date");
  expect(screen.queryByText("SECRET")).not.toBeInTheDocument();
  expect(screen.queryByText("Person's decision: Approved")).not.toBeInTheDocument();
});
it("supports manual deleted IDs and neutral empty history without a live user lookup", async () => {
  vi.mocked(ContactHistory.list).mockResolvedValue({ entries: [] });
  mount("/admin/contact-history");
  expect(screen.getByText("Choose a person")).toBeInTheDocument();
  await userEvent.type(screen.getByLabelText("Person ID"), "9223372036854775807");
  await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(await screen.findByText("No matching history")).toBeInTheDocument();
  expect(ContactHistory.list).toHaveBeenCalledWith("9223372036854775807", empty);
  expect(screen.getByRole("button", { name: "Export all matching CSV" })).toBeEnabled();
});
it("rejects overflow without coercion and preserves all draft fields", async () => {
  mount("/admin/contact-history");
  await userEvent.type(screen.getByLabelText("Person ID"), "9223372036854775808");
  await userEvent.click(screen.getByLabelText("Direct contact"));
  await userEvent.type(screen.getByLabelText("Until (optional)"), "2026-11-01T07:00:00Z");
  await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(screen.getByText(/Enter a person ID from/)).toBeInTheDocument();
  expect(screen.getByLabelText("Person ID")).toHaveValue("9223372036854775808");
  expect(screen.getByLabelText("Person ID")).toHaveFocus();
  expect(screen.getByLabelText("Direct contact")).toBeChecked(); expect(ContactHistory.list).not.toHaveBeenCalled();
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
  if (change === "filter") await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
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
