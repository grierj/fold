import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { Adds } from "../add-api";
import { ApiError } from "../api";
import { addRequest } from "../test/addRequest";
import { AddRequestsPage } from "./AddRequests";

const actor = vi.hoisted(() => ({ user: { id: 2 } as { id: number } | null }));
vi.mock("../auth", () => ({ useAuth: () => actor }));
vi.mock("../add-api", () => ({ Adds: { request: vi.fn(), requests: vi.fn(), decide: vi.fn(), withdraw: vi.fn(), complete: vi.fn() } }));
function mount(path = "/add-requests/7") { return render(<MemoryRouter initialEntries={[path]}><Routes><Route path="/add-requests" element={<AddRequestsPage />} /><Route path="/add-requests/:id" element={<AddRequestsPage />} /><Route path="/login" element={<p>Sign in first</p>} /></Routes></MemoryRouter>); }
beforeEach(() => { vi.resetAllMocks(); actor.user = { id: 2 }; vi.mocked(Adds.request).mockResolvedValue({ request: addRequest() }); vi.mocked(Adds.requests).mockResolvedValue({ requests: [] }); });

it.each(["group_add", "occurrence_add"] as const)("supports both teen decision orders in %s, including an admin requester without preapproval", async (purpose) => {
  for (const first of ["admin", "subject"] as const) {
    let current = addRequest({ purpose, canAdminDecide: first === "admin", canSubjectDecide: first === "subject" });
    vi.mocked(Adds.request).mockImplementation(async () => ({ request: current }));
    vi.mocked(Adds.decide).mockImplementation(async (_id, party, decision) => {
      const last = current.subjectDecision === "approved" || current.adminDecision === "approved";
      current = { ...current, [party === "subject" ? "subjectDecision" : "adminDecision"]: decision, canAdminDecide: !last && party !== "admin", canSubjectDecide: !last && party !== "subject", status: last ? "granted" : "pending" };
      return { request: current };
    });
    const view = mount();
    await userEvent.click(await screen.findByRole("button", { name: "Approve adding" }));
    expect(screen.getByRole("status")).toHaveTextContent("Not added yet");
    expect(screen.getByRole("heading", { name: `${first === "admin" ? "Admin's" : "Teen's"} decision: Approved` })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Approve adding" }));
    expect(screen.getByRole("status")).toHaveTextContent(/^Added$/);
    expect(Adds.decide).toHaveBeenLastCalledWith(7, first === "admin" ? "subject" : "admin", "approved");
    view.unmount();
  }
});

it.each(["subject", "admin"] as const)("a %s denial hides all actions even with stale capabilities", async (party) => {
  vi.mocked(Adds.request).mockResolvedValue({ request: addRequest({ canSubjectDecide: party === "subject", canAdminDecide: party === "admin" }) });
  vi.mocked(Adds.decide).mockResolvedValue({ request: addRequest({ status: "denied", subjectDecision: "denied", canSubjectDecide: true, canAdminDecide: true, canComplete: true, canWithdraw: true }) });
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Deny ask" }));
  expect(screen.getByRole("status")).toHaveTextContent("Denied"); expect(screen.queryByRole("button", { name: /Approve|Retry|Withdraw/ })).not.toBeInTheDocument();
});

it.each(["capacity", "childcare", "options", "throttled"] as const)("retains approved pending state with actionable %s retry copy and original expiry", async (completionProblem) => {
  const ready = addRequest({ subjectDecision: "approved", adminDecision: "approved", canComplete: true, canWithdraw: true });
  vi.mocked(Adds.request).mockResolvedValue({ request: ready });
  vi.mocked(Adds.complete).mockResolvedValueOnce({ request: { ...ready, completionProblem } }).mockResolvedValueOnce({ request: { ...ready, status: "granted", canComplete: false } });
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Retry adding" }));
  expect(screen.getByRole("status")).toHaveTextContent("Not added yet"); expect(screen.getByText(/Approval was recorded/)).toBeInTheDocument();
  expect(screen.getByText(/Expires/)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Retry adding" }));
  expect(screen.getByRole("status")).toHaveTextContent(/^Added$/); expect(Adds.complete).toHaveBeenCalledTimes(2);
});

it("offers only safe hidden previews and never constructs event or hidden group links", async () => {
  vi.mocked(Adds.requests).mockResolvedValue({ requests: [addRequest({ reason: "Group reason" }), addRequest({ id: 8, purpose: "occurrence_add", eventTitle: "One requested date", startsAt: "2026-12-01T19:00:00Z", reason: "MUST NOT RENDER", contextId: 99, groupName: "MUST NOT RENDER", requesterName: "MUST NOT RENDER", subjectName: "MUST NOT RENDER" })] });
  mount("/add-requests"); await screen.findByText("One requested date");
  expect(screen.getByText("A group membership")).toBeInTheDocument(); expect(screen.queryByText("MUST NOT RENDER")).not.toBeInTheDocument();
  expect(screen.getAllByRole("link").map((a) => a.getAttribute("href"))).toEqual(["/add-requests/7", "/add-requests/8"]);
});

it("does not show a date reason or synthetic navigation on detail", async () => {
  vi.mocked(Adds.request).mockResolvedValue({ request: addRequest({ purpose: "occurrence_add", eventTitle: "One date", reason: "SECRET", contextId: 99, groupName: "SECRET", requesterName: "SECRET" }) });
  mount(); await screen.findByText("One date"); expect(screen.queryByText("SECRET")).not.toBeInTheDocument();
  expect(screen.getAllByRole("link")).toHaveLength(1);
});

it("withdraws explicitly and clears disclosed data when authority is removed", async () => {
  vi.mocked(Adds.request).mockResolvedValueOnce({ request: addRequest({ groupName: "Visible group", contextId: 3, canWithdraw: true }) }).mockRejectedValue(new ApiError(404, "SECRET"));
  vi.mocked(Adds.withdraw).mockResolvedValue({ request: addRequest({ status: "withdrawn" }) });
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Withdraw ask" }));
  expect(Adds.withdraw).toHaveBeenCalledWith(7); expect(screen.getByRole("status")).toHaveTextContent("Withdrawn");
  fireEvent(window, new Event("focus"));
  expect(await screen.findByText("This ask is not available")).toBeInTheDocument(); expect(screen.queryByText("Visible group")).not.toBeInTheDocument();
});

it("uses exclusive pagination and clears history on failed focus refresh", async () => {
  vi.mocked(Adds.requests).mockResolvedValueOnce({ requests: Array.from({ length: 100 }, (_, i) => addRequest({ id: 300 - i })) }).mockResolvedValueOnce({ requests: [addRequest({ id: 200 })] }).mockRejectedValue(new Error("SECRET"));
  mount("/add-requests"); await userEvent.click(await screen.findByRole("button", { name: "Load older asks" })); expect(Adds.requests).toHaveBeenLastCalledWith(201);
  expect(screen.getAllByRole("listitem")).toHaveLength(101);
  fireEvent(window, new Event("focus")); await screen.findByRole("alert"); expect(screen.queryByRole("listitem")).not.toBeInTheDocument();
});

it("ignores a late decision after a focus refresh loses authority", async () => {
  let finish!: (value: Awaited<ReturnType<typeof Adds.decide>>) => void;
  vi.mocked(Adds.request).mockResolvedValueOnce({ request: addRequest({ canSubjectDecide: true }) }).mockRejectedValue(new ApiError(404, "private"));
  vi.mocked(Adds.decide).mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
  mount(); await userEvent.click(await screen.findByRole("button", { name: "Approve adding" }));
  fireEvent(window, new Event("focus")); await screen.findByText("This ask is not available");
  await act(async () => finish({ request: addRequest({ groupName: "STALE SECRET", status: "granted" }) })); expect(screen.queryByText("STALE SECRET")).not.toBeInTheDocument();
});

it("sends signed out viewers to login rather than loading review data", async () => {
  actor.user = null; mount(); expect(await screen.findByText("Sign in first")).toBeInTheDocument(); expect(Adds.request).not.toHaveBeenCalled();
});

it("does not paginate a deferred refresh with an empty cursor or append duplicate first-page rows", async () => {
  let finish!: (value: Awaited<ReturnType<typeof Adds.requests>>) => void;
  const rows = Array.from({ length: 100 }, (_, i) => addRequest({ id: 300 - i }));
  vi.mocked(Adds.requests).mockResolvedValueOnce({ requests: rows }).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; })).mockResolvedValue({ requests: [] });
  mount("/add-requests"); await screen.findByRole("button", { name: "Load older asks" });
  fireEvent(window, new Event("focus"));
  expect(screen.queryByRole("button", { name: "Load older asks" })).not.toBeInTheDocument();
  expect(screen.queryByRole("listitem")).not.toBeInTheDocument();
  expect(Adds.requests).toHaveBeenCalledTimes(2);
  await act(async () => finish({ requests: rows }));
  expect(screen.getAllByRole("listitem")).toHaveLength(100);
  await userEvent.click(screen.getByRole("button", { name: "Load older asks" }));
  expect(Adds.requests).toHaveBeenLastCalledWith(201);
  expect(screen.getAllByRole("listitem")).toHaveLength(100);
});
