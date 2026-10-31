import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RouterProvider, createMemoryRouter } from "react-router-dom";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, Site } from "../api";
import { Messages } from "../messages-api";
import { UnsavedChangesBlocker } from "../lib/unsavedChanges";
import { ERROR_COPY } from "../lib/errors";
import { polyfillEditorLayout } from "../test/editor";
import { adminRecipients, composeOptions, message, recipients, summary } from "../test/compose";
import { wallClock, wallToInstant } from "../lib/when";
import { MessageComposerPage } from "./MessageComposer";

// Two days ahead on the church's clock, within the year the API allows.
const soonDay = () => wallClock(new Date(Date.now() + 2 * 864e5).toISOString(), "America/Boise").day;

const viewer = vi.hoisted(() => ({ isAdmin: false }));
vi.mock("../auth", () => ({ useAuth: () => ({ user: { id: 2, name: "Lee Leader", displayName: "", email: "lee@example.com" }, loading: false, isAdmin: viewer.isAdmin, canCompose: true }) }));
vi.mock("../api", async (importActual) => ({
  ...(await importActual<typeof import("../api")>()),
  Site: { get: vi.fn().mockResolvedValue({ name: "Grace", timezone: "America/Boise" }) },
}));
vi.mock("../messages-api", () => ({
  previewHTMLPath: (id: number) => `/api/v1/messages/${id}/preview.html`,
  Messages: {
    options: vi.fn(), audiencePreview: vi.fn(), get: vi.fn(), create: vi.fn(), update: vi.fn(),
    schedule: vi.fn(), cancel: vi.fn(), preview: vi.fn(), test: vi.fn(), recipients: vi.fn(), retry: vi.fn(),
  },
}));

beforeAll(polyfillEditorLayout);

function open(path: string) {
  const router = createMemoryRouter(
    [
      { path: "/messages/new", element: <><UnsavedChangesBlocker /><MessageComposerPage /></> },
      { path: "/messages/:id", element: <><UnsavedChangesBlocker /><MessageComposerPage /></> },
      { path: "/messages", element: <p>Messages list</p> },
    ],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

beforeEach(() => {
  vi.mocked(Messages.options).mockResolvedValue(composeOptions());
  vi.mocked(Messages.audiencePreview).mockResolvedValue({ summary: summary() });
  vi.mocked(Messages.get).mockResolvedValue({ message: message() });
  vi.mocked(Messages.update).mockImplementation(async (id, d) => ({ message: message({ id, subject: d.subject, bodyHtml: d.bodyHtml, audience: d.audience }) }));
  vi.mocked(Messages.create).mockImplementation(async (d) => ({ message: message({ id: 9, subject: d.subject, bodyHtml: d.bodyHtml, audience: d.audience }) }));
  vi.mocked(Messages.preview).mockResolvedValue({ preview: { subject: "Bring a friend", text: "Hi Lee\n\nYou're getting messages from leaders" } });
  vi.mocked(Messages.test).mockResolvedValue({ ok: true });
  vi.mocked(Messages.recipients).mockResolvedValue(recipients());
});
afterEach(() => { vi.clearAllMocks(); viewer.isAdmin = false; });

const editorReady = () => screen.findByRole("textbox", { name: "Message" });

describe("MessageComposerPage", () => {
  it("starts from the group it was opened for, with a live count", async () => {
    open("/messages/new?group=7");
    expect(await screen.findByRole("heading", { name: "New message", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Send to" })).toHaveValue("group:7");
    expect(await screen.findByText("3 people; 1 won't get it: 1 turned off messages from leaders")).toBeInTheDocument();
    expect(Messages.audiencePreview).toHaveBeenCalledWith({ type: "group", groupId: 7, roles: ["member", "leader"] });
    // One primary action.
    await editorReady();
    expect(screen.getByRole("button", { name: "Send now" })).toHaveClass("btn-primary");
    expect(document.querySelectorAll(".btn-primary")).toHaveLength(1);
  });

  it("says what is missing under each field and focuses the first", async () => {
    const user = userEvent.setup();
    open("/messages/new");
    await editorReady();
    await user.click(screen.getByRole("button", { name: "Send now" }));
    const to = screen.getByRole("combobox", { name: "Send to" });
    expect(to).toHaveAccessibleDescription(expect.stringContaining(ERROR_COPY.message_audience));
    expect(to).toHaveFocus();
    expect(screen.getByRole("textbox", { name: "Subject" })).toHaveAccessibleDescription(expect.stringContaining("Add a subject."));
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveAccessibleDescription(expect.stringContaining("Write your message."));
    expect(Messages.create).not.toHaveBeenCalled();
  });

  it("saves a new draft and moves to its address without reloading it", async () => {
    const user = userEvent.setup();
    const router = open("/messages/new?event=3");
    await editorReady();
    await user.type(screen.getByRole("textbox", { name: "Subject" }), "Snacks");
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Draft saved.")).toBeInTheDocument();
    expect(Messages.create).toHaveBeenCalledWith({ subject: "Snacks", bodyHtml: "", audience: { type: "event_series", eventId: 3, statuses: ["going", "waitlisted"] } });
    await waitFor(() => expect(router.state.location.pathname).toBe("/messages/9"));
    expect(Messages.get).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: "Subject" })).toHaveValue("Snacks");
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(Messages.update).toHaveBeenCalledWith(9, expect.objectContaining({ subject: "Snacks" })));
  });

  it("previews the saved email in a sandboxed frame, with the plain text", async () => {
    const user = userEvent.setup();
    open("/messages/5");
    await editorReady();
    await user.click(screen.getByRole("button", { name: "Preview" }));
    const frame = await screen.findByTitle("Email preview");
    expect(Messages.update).toHaveBeenCalled();
    expect(frame).toHaveAttribute("src", "/api/v1/messages/5/preview.html");
    expect(frame).toHaveAttribute("sandbox", "allow-popups allow-popups-to-escape-sandbox");
    expect(screen.getByText("Plain-text version")).toBeInTheDocument();
    expect(screen.getByText(/You're getting messages from leaders/)).toBeInTheDocument();
  });

  it("sends a test to the author and says where", async () => {
    const user = userEvent.setup();
    open("/messages/5");
    await editorReady();
    await user.click(screen.getByRole("button", { name: "Send a test to me" }));
    expect(await screen.findByText("Sent to you at lee@example.com.")).toBeInTheDocument();
    expect(Messages.test).toHaveBeenCalledWith(5);
  });

  it("shows the API's error in words, never its text", async () => {
    const user = userEvent.setup();
    vi.mocked(Messages.test).mockRejectedValue(new ApiError(400, "you've sent 10 tests in the last hour", { code: "message_test_limit" }));
    vi.mocked(Messages.update).mockRejectedValueOnce(new ApiError(400, "event merge fields only work…", { code: "message_merge_event" }));
    open("/messages/5");
    await editorReady();
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText(ERROR_COPY.message_merge_event)).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveAttribute("aria-invalid", "true");
    await user.click(screen.getByRole("button", { name: "Send a test to me" }));
    expect(await screen.findByText(ERROR_COPY.message_test_limit)).toBeInTheDocument();
    expect(screen.queryByText(/you've sent 10 tests in the last hour$/)).toBeNull();
  });

  it("schedules for a picked time after confirming the count", async () => {
    const user = userEvent.setup();
    vi.mocked(Messages.schedule).mockImplementation(async (id, at) => ({ message: message({ id, status: "scheduled", scheduledAt: at ?? null }), summary: summary() }));
    open("/messages/5");
    await editorReady();
    await user.click(screen.getByRole("radio", { name: "Pick a date and time" }));
    expect(screen.getByText(/Times are in Mountain time/)).toBeInTheDocument();
    const day = soonDay();
    fireEvent.change(screen.getByLabelText("Date"), { target: { value: day } });
    fireEvent.change(screen.getByLabelText("Time"), { target: { value: "09:30" } });
    await user.click(screen.getByRole("button", { name: "Schedule" }));
    const ask = await screen.findByRole("group", { name: /^Send to 2 people on / });
    expect(Messages.schedule).not.toHaveBeenCalled();
    await user.click(within(ask).getByRole("button", { name: "Schedule" }));
    expect(Messages.schedule).toHaveBeenCalledWith(5, wallToInstant(day, "09:30", "America/Boise"));
    expect(await screen.findByText(/^Scheduled for /)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel message" })).toHaveClass("btn-danger-outline");
  });

  it("asks before sending now, and Go back leaves it a draft", async () => {
    const user = userEvent.setup();
    open("/messages/5");
    await editorReady();
    await user.click(screen.getByRole("button", { name: "Send now" }));
    const ask = await screen.findByRole("group", { name: "Send to 2 people now?" });
    expect(within(ask).getByRole("button", { name: "Send now" })).toHaveFocus();
    await user.click(within(ask).getByRole("button", { name: "Go back" }));
    expect(screen.queryByRole("group", { name: "Send to 2 people now?" })).toBeNull();
    expect(Messages.schedule).not.toHaveBeenCalled();
  });

  it("refuses a time in the past before saving", async () => {
    const user = userEvent.setup();
    open("/messages/5");
    await editorReady();
    await user.click(screen.getByRole("radio", { name: "Pick a date and time" }));
    fireEvent.change(screen.getByLabelText("Date"), { target: { value: "2020-01-01" } });
    await user.click(screen.getByRole("button", { name: "Schedule" }));
    expect(screen.getByLabelText("Date")).toHaveAccessibleDescription(ERROR_COPY.message_when);
    expect(Messages.update).not.toHaveBeenCalled();
  });

  it("says no one would get it instead of asking", async () => {
    const user = userEvent.setup();
    open("/messages/5");
    await editorReady();
    vi.mocked(Messages.audiencePreview).mockResolvedValue({ summary: summary({ people: 1, eligible: 0, teens: 0, skipped: { "no email address": 1 } }) });
    await user.click(screen.getByRole("button", { name: "Send now" }));
    expect(await screen.findByText(ERROR_COPY.message_nobody)).toBeInTheDocument();
  });

  it("shows a scheduled message with Edit and Cancel message, and cancels it", async () => {
    const user = userEvent.setup();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "scheduled", scheduledAt: "2030-11-05T16:30:00Z" }) });
    vi.mocked(Messages.cancel).mockResolvedValue({ message: message({ status: "cancelled", scheduledAt: "2030-11-05T16:30:00Z", editable: false }) });
    open("/messages/5");
    expect(await screen.findByRole("heading", { name: "Bring a friend", level: 1 })).toBeInTheDocument();
    expect(screen.getByText(/^Scheduled for /)).toBeInTheDocument();
    expect(screen.getByText("Book club (Members, Leaders)")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(await editorReady()).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Pick a date and time" })).toBeChecked();
    expect(screen.getByLabelText("Date")).toHaveValue("2030-11-05");
    expect(screen.getByText("Your changes save when you preview or send a test. The message stays scheduled.")).toBeInTheDocument();
    const asked = vi.mocked(window.confirm).mock.calls.length;
    await user.click(screen.getByRole("button", { name: "Done editing" }));
    // Nothing changed, so nothing to ask about.
    expect(vi.mocked(window.confirm).mock.calls.length).toBe(asked);
    await user.click(screen.getByRole("button", { name: "Cancel message" }));
    expect(await screen.findByText("Cancelled")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
  });

  it("withdraws the question when anything changes after it is asked", async () => {
    const user = userEvent.setup();
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    open("/messages/5");
    await editorReady();
    await user.click(screen.getByRole("button", { name: "Send now" }));
    const ask = await screen.findByRole("group", { name: "Send to 2 people now?" });
    expect(scroll).toHaveBeenCalledWith({ block: "center" });
    await user.type(screen.getByRole("textbox", { name: "Subject" }), "!");
    expect(ask).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: /^Send to / })).toBeNull();
    expect(screen.getByRole("button", { name: "Send now" })).toHaveAttribute("type", "submit");
    // The same for the audience and the when.
    await user.click(screen.getByRole("button", { name: "Send now" }));
    await screen.findByRole("group", { name: "Send to 2 people now?" });
    await user.click(screen.getByRole("button", { name: "Leaders" }));
    expect(screen.queryByRole("group", { name: /^Send to / })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Send now" }));
    await screen.findByRole("group", { name: "Send to 2 people now?" });
    await user.click(screen.getByRole("radio", { name: "Pick a date and time" }));
    expect(screen.queryByRole("group", { name: /^Send to / })).toBeNull();
    expect(Messages.schedule).not.toHaveBeenCalled();
  });

  it("scrolls the preview into view when it opens", async () => {
    const user = userEvent.setup();
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    open("/messages/5");
    await editorReady();
    await user.click(screen.getByRole("button", { name: "Preview" }));
    await screen.findByTitle("Email preview");
    expect(scroll).toHaveBeenCalledWith({ block: "start" });
  });

  it("shows a message that started sending meanwhile read only", async () => {
    const user = userEvent.setup();
    vi.mocked(Messages.update).mockRejectedValueOnce(new ApiError(409, "this message is already sending or sent", { code: "message_locked" }));
    open("/messages/5");
    await editorReady();
    vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "sending", editable: false }) });
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Sending")).toBeInTheDocument();
    expect(screen.getByText(ERROR_COPY.message_locked)).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Subject" })).toBeNull();
    expect(Messages.get).toHaveBeenCalledTimes(2);
  });

  it("waits for the church's zone before filling a scheduled time", async () => {
    const user = userEvent.setup();
    let answer!: (v: unknown) => void;
    const site = new Promise((r) => { answer = r; });
    vi.mocked(Site.get).mockReturnValue(site as never);
    vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "scheduled", scheduledAt: "2030-11-05T16:30:00Z" }) });
    open("/messages/5");
    await waitFor(() => expect(Messages.get).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    await act(async () => { answer({ name: "Grace", timezone: "Asia/Kolkata" }); });
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await editorReady();
    expect(screen.getByLabelText("Date")).toHaveValue("2030-11-05");
    expect(screen.getByLabelText("Time")).toHaveValue("22:00");
    vi.mocked(Site.get).mockResolvedValue({ name: "Grace", timezone: "America/Boise" } as never);
  });

  it("discards a saved draft after asking", async () => {
    const user = userEvent.setup();
    const ask = vi.spyOn(window, "confirm").mockReturnValue(true);
    vi.mocked(Messages.cancel).mockResolvedValue({ message: message({ status: "cancelled", editable: false }) });
    const router = open("/messages/5");
    await editorReady();
    const discard = screen.getByRole("button", { name: "Discard draft" });
    expect(discard).toHaveClass("btn-danger-outline");
    await user.click(discard);
    expect(ask).toHaveBeenCalledWith("Discard this draft? You can't undo this.");
    expect(Messages.cancel).toHaveBeenCalledWith(5);
    await waitFor(() => expect(router.state.location.pathname).toBe("/messages"));
    ask.mockRestore();
  });

  it("offers no Discard draft before the first save", async () => {
    open("/messages/new?group=7");
    await editorReady();
    expect(screen.queryByRole("button", { name: "Discard draft" })).toBeNull();
  });

  it("guards edits made after the first save moves to the draft's address", async () => {
    const user = userEvent.setup();
    const ask = vi.spyOn(window, "confirm").mockReturnValue(false);
    const router = open("/messages/new?group=7");
    await editorReady();
    await user.type(screen.getByRole("textbox", { name: "Subject" }), "Snacks");
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/messages/9"));
    expect(ask).not.toHaveBeenCalled();
    await user.type(screen.getByRole("textbox", { name: "Subject" }), " now");
    await act(async () => { await router.navigate("/messages"); });
    expect(ask).toHaveBeenCalledWith("Leave without saving? Your changes will be lost.");
    expect(router.state.location.pathname).toBe("/messages/9");
    ask.mockRestore();
  });

  it("is read only once sent", async () => {
    vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "sent", sentAt: "2026-10-30T16:30:00Z", editable: false }) });
    open("/messages/5");
    expect(await screen.findByText(/^Sent (?!to )/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(await screen.findByText("{{first_name}}", { exact: false })).toBeInTheDocument();
  });

  it("tells a member the composer isn't theirs", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions({ canCompose: false, groups: [], events: [] }));
    open("/messages/new");
    expect(await screen.findByRole("heading", { name: "Messages are for leaders" })).toBeInTheDocument();
  });

  it("says a message it can't find isn't here", async () => {
    vi.mocked(Messages.get).mockRejectedValue(new ApiError(404, "message not found", { code: "not_found" }));
    open("/messages/77");
    expect(await screen.findByRole("heading", { name: "This message isn't here" })).toBeInTheDocument();
  });

  it("shows the text tab only when texting is set up", async () => {
    vi.mocked(Messages.options).mockResolvedValue(composeOptions({ smsEnabled: true }));
    open("/messages/5");
    await editorReady();
    await act(async () => { fireEvent.click(screen.getByRole("tab", { name: "Text" })); });
    expect(screen.getByRole("textbox", { name: "Text message" })).toBeInTheDocument();
  });

  it("has no text tab today", async () => {
    open("/messages/5");
    await editorReady();
    expect(screen.queryByRole("tab", { name: "Text" })).toBeNull();
  });
});

// After it goes out (M3.5b): the counts in words, who got it, a failed
// send's error with Try again, and polling while it sends.
describe("a sent message's history", () => {
  const sent = (over: Partial<Parameters<typeof message>[0]> = {}) =>
    message({ status: "sent", sentAt: "2026-10-30T16:30:00Z", editable: false, ...over });
  const counts = (over: Partial<ReturnType<typeof recipients>["counts"]>) => ({ ...recipients().counts, ...over });

  it("words the counts and lists who got it with Teen badges, guardian copies only as a count", async () => {
    const user = userEvent.setup();
    vi.mocked(Messages.get).mockResolvedValue({ message: sent() });
    vi.mocked(Messages.recipients).mockResolvedValue(recipients({ counts: counts({ bounced: 1 }) }));
    open("/messages/5");
    expect(await screen.findByText("Sent to 2 people · 1 bounced · 1 skipped (1 turned off messages from leaders)")).toBeInTheDocument();
    expect(screen.getByText("1 of the people it was for is a teen.")).toBeInTheDocument();
    expect(screen.getByText("1 guardian copy sent")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    await user.click(screen.getByText("See who got it"));
    const rows = within(screen.getByRole("list")).getAllByRole("listitem");
    expect(rows.map((r) => r.textContent)).toEqual([
      "Ann Adult· Sent",
      "Opal Opted· Didn't get it",
      "Tia TeenTeen· Sent",
    ]);
    expect(screen.queryByText(/Copy for/)).toBeNull();
  });

  it("shows an admin each guardian's copy, each person's reason and how many more there are", async () => {
    viewer.isAdmin = true;
    const user = userEvent.setup();
    vi.mocked(Messages.get).mockResolvedValue({ message: sent() });
    vi.mocked(Messages.recipients).mockResolvedValue(adminRecipients({ counts: counts({ sent: 250 }), more: true }));
    open("/messages/5");
    await user.click(await screen.findByText("See who got it"));
    const rows = within(screen.getByRole("list")).getAllByRole("listitem");
    expect(rows.map((r) => r.textContent)).toEqual([
      "Ann Adult· Sent",
      "Opal Opted· Didn't get it: turned off messages from leaders",
      "Pat ParentCopy for Tia Teen· Sent",
      "Tia TeenTeen· Sent",
      // 250 sent, 1 skipped and 1 copy, less the 4 shown.
      "And 248 more",
    ]);
  });

  it("shows a failed send's error under the title, and Try again asks before resending", async () => {
    const user = userEvent.setup();
    vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "failed", editable: false, failedCount: 4, sentAt: "2026-10-31T15:30:00Z", error: "The email service didn't take it. Try again in a little while. If it keeps happening, ask an admin to open this message to see what went wrong." }) });
    vi.mocked(Messages.recipients).mockResolvedValue(recipients({
      counts: counts({ sent: 0, failed: 4, skipped: 0, teens: 0, guardianCopies: 0 }), skippedBy: {}, canRetry: true,
      recipients: [{ name: "Ann Adult", teen: false, status: "missed" }],
    }));
    open("/messages/5");
    // The problem is announced; Try again sits outside the announced part.
    const alert = (await screen.findByText("This message didn't go out")).closest("[role=alert]") as HTMLElement;
    expect(alert).toHaveTextContent("The email service didn't take it.");
    expect(within(alert).queryByRole("button")).toBeNull();
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(await screen.findByText("4 didn't get it")).toBeInTheDocument();
    const again = await screen.findByRole("button", { name: "Try again" });
    expect(again).toHaveClass("btn-primary");
    expect(document.querySelectorAll(".btn-primary")).toHaveLength(1);

    const ask = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    await user.click(again);
    expect(ask).toHaveBeenLastCalledWith("Send again to the 4 people it didn't reach?");
    expect(Messages.retry).not.toHaveBeenCalled();
    vi.mocked(Messages.retry).mockResolvedValue({ message: message({ status: "sending", editable: false, sentAt: "2026-10-31T15:30:00Z", updatedAt: "2026-10-31T16:00:00Z" }) });
    vi.mocked(Messages.recipients).mockResolvedValue(recipients({ counts: counts({ sent: 0, queued: 4, skipped: 0 }), skippedBy: {} }));
    await user.click(again);
    expect(Messages.retry).toHaveBeenCalledWith(5);
    expect(await screen.findByText("Sending again… 0 of 4 so far")).toBeInTheDocument();
    // Checking again: nothing is announced until it settles again.
    expect(document.querySelector(".sr-only[role=status]")?.textContent).toBe("");
    expect(screen.queryByText("This message didn't go out")).toBeNull();
    ask.mockRestore();
  });

  it("counts a partly sent message's people and guardian copies apart, in the banner and the confirm", async () => {
    const user = userEvent.setup();
    vi.mocked(Messages.get).mockResolvedValue({ message: sent({ failedCount: 2, error: "The email service didn't take it." }) });
    vi.mocked(Messages.recipients).mockResolvedValue(recipients({ counts: counts({ failed: 1, guardianCopiesMissed: 1, guardianCopiesFailed: 1 }), canRetry: true }));
    open("/messages/5");
    // Some went out: a status, not an alert.
    expect((await screen.findByText("1 person didn't get this")).closest("[role=status]")).not.toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("1 guardian copy didn't go out")).toBeInTheDocument();
    expect(screen.getByText(/^Partly sent /)).toBeInTheDocument();
    const ask = vi.spyOn(window, "confirm").mockReturnValue(false);
    await user.click(await screen.findByRole("button", { name: "Try again" }));
    expect(ask).toHaveBeenCalledWith("Send again to the 1 person and 1 guardian copy it didn't reach?");
    ask.mockRestore();
  });

  it("checks every 5 seconds while sending, and says once that it settled", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "sending", editable: false }) });
      vi.mocked(Messages.recipients).mockResolvedValue(recipients({ counts: counts({ sent: 1, queued: 2, skipped: 0 }) }));
      open("/messages/5");
      expect(await screen.findByText("Sending… 1 of 3 so far")).toBeInTheDocument();
      const announced = () => document.querySelector(".sr-only[role=status]")?.textContent;
      vi.mocked(Messages.recipients).mockResolvedValue(recipients({ counts: counts({ sent: 2, queued: 1, skipped: 0 }) }));
      await act(() => vi.advanceTimersByTimeAsync(5000));
      expect(await screen.findByText("Sending… 2 of 3 so far")).toBeInTheDocument();
      expect(announced()).toBe("");
      vi.mocked(Messages.get).mockResolvedValue({ message: sent({ updatedAt: "2026-10-31T16:00:00Z" }) });
      vi.mocked(Messages.recipients).mockResolvedValue(recipients({ counts: counts({ skipped: 0 }) }));
      await act(() => vi.advanceTimersByTimeAsync(5000));
      await waitFor(() => expect(announced()).toBe("Sent to 2 people"));
      expect(screen.getAllByText("Sent to 2 people")).toHaveLength(2);
      const gets = vi.mocked(Messages.get).mock.calls.length;
      await act(() => vi.advanceTimersByTimeAsync(20000));
      expect(Messages.get).toHaveBeenCalledTimes(gets);
    } finally {
      vi.useRealTimers();
    }
  });

  it("waits while the tab is hidden and checks again when it comes back", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    try {
      vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "sending", editable: false }) });
      open("/messages/5");
      expect(await screen.findByText(/^Sending… /)).toBeInTheDocument();
      const gets = vi.mocked(Messages.get).mock.calls.length;
      await act(() => vi.advanceTimersByTimeAsync(30000));
      expect(Messages.get).toHaveBeenCalledTimes(gets);
      hidden.mockReturnValue(false);
      await act(async () => { document.dispatchEvent(new Event("visibilitychange")); });
      await waitFor(() => expect(Messages.get).toHaveBeenCalledTimes(gets + 1));
    } finally {
      hidden.mockRestore();
      vi.useRealTimers();
    }
  });

  it("starts checking when a scheduled message comes due with the page open", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const at = new Date(Date.now() + 10 * 60_000).toISOString();
      vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "scheduled", scheduledAt: at, editable: true }) });
      open("/messages/5");
      expect(await screen.findByText(/^Scheduled for /)).toBeInTheDocument();
      const gets = vi.mocked(Messages.get).mock.calls.length;
      await act(() => vi.advanceTimersByTimeAsync(8 * 60_000));
      expect(Messages.get).toHaveBeenCalledTimes(gets);
      vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "sending", editable: false, scheduledAt: at, updatedAt: "2026-10-31T16:00:00Z" }) });
      await act(() => vi.advanceTimersByTimeAsync(2 * 60_000));
      // The page catches up once it is due, then checks 5 seconds later.
      await act(() => vi.advanceTimersByTimeAsync(5000));
      expect(await screen.findByText(/^Sending… /)).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops after 30 minutes with nothing changing and offers to check again", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "sending", editable: false }) });
      open("/messages/5");
      expect(await screen.findByText(/^Sending… /)).toBeInTheDocument();
      await act(() => vi.advanceTimersByTimeAsync(31 * 60_000));
      const refresh = await screen.findByRole("button", { name: "Refresh to check again" });
      const gets = vi.mocked(Messages.get).mock.calls.length;
      await act(() => vi.advanceTimersByTimeAsync(60_000));
      expect(Messages.get).toHaveBeenCalledTimes(gets);
      await user.click(refresh);
      await act(() => vi.advanceTimersByTimeAsync(5000));
      expect(Messages.get).toHaveBeenCalledTimes(gets + 1);
      expect(screen.queryByRole("button", { name: "Refresh to check again" })).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops checking when the page goes away", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      vi.mocked(Messages.get).mockResolvedValue({ message: message({ status: "sending", editable: false }) });
      const router = open("/messages/5");
      expect(await screen.findByText(/^Sending… /)).toBeInTheDocument();
      await act(() => router.navigate("/messages"));
      const gets = vi.mocked(Messages.get).mock.calls.length;
      await act(() => vi.advanceTimersByTimeAsync(20000));
      expect(Messages.get).toHaveBeenCalledTimes(gets);
    } finally {
      vi.useRealTimers();
    }
  });
});
