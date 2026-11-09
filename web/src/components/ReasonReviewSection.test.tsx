import { act, cleanup, render, screen, within } from "@testing-library/react";
import { Profiler } from "react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { KEEP, REMOVE, ReasonReviewStore } from "../lib/reasonReviewStore";
import type { ErasureReasonItem } from "../types";
import { QC1_WEB } from "../test/qc1Facts";
import { ReasonProgress, ReasonReviewSection, ReasonSummary } from "./ReasonReviewSection";

// Reasons shown per page.
const DISPLAY_PAGE = 50;

// Counts how many times a row renders: each row formats its date once per render.
const rowRenders = vi.hoisted(() => ({ n: 0 }));
vi.mock("../lib/when", async (orig) => ({ ...(await orig<typeof import("../lib/when")>()), formatShortDay: () => { rowRenders.n++; return "Nov 1"; } }));

const item = (id: number): ErasureReasonItem => ({
  requestId: String(id), reviewHash: (id % 0xffff).toString(16).padStart(4, "0").repeat(16), role: "subject", purpose: "contact", status: "pending",
  createdAt: "2026-11-01T12:00:00Z", counterpartName: "Lee", counterpartDeleted: false, reviewerIsParty: false, reason: `Reason text ${id}`,
});

function filled(n: number): ReasonReviewStore {
  const store = new ReasonReviewStore();
  for (let from = 1; from <= n; from += 100) store.appendPage(Array.from({ length: Math.min(100, n - from + 1) }, (_, i) => item(from + i)));
  return store;
}

function show(store: ReasonReviewStore, props: Partial<React.ComponentProps<typeof ReasonReviewSection>> = {}) {
  return render(
    <>
      <ReasonReviewSection store={store} disabled={false} loading={false} jump={null} {...props} />
      <ReasonProgress store={store} done />
      <ReasonSummary store={store} />
    </>,
  );
}

afterEach(() => {
  cleanup();
  rowRenders.n = 0;
});

describe("ReasonReviewSection", () => {
  it("shows 50 reasons of 120 with global numbers, and nothing preselected", () => {
    show(filled(120));
    expect(screen.getByText("Reasons 1 to 50 of 120")).toBeInTheDocument();
    expect(screen.getAllByRole("radio")).toHaveLength(2 * DISPLAY_PAGE);
    for (const r of screen.getAllByRole("radio")) expect(r).not.toBeChecked();
    expect(screen.getByRole("group", { name: "Reason 1 of 120" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Reason 50 of 120" })).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Reason 51 of 120" })).not.toBeInTheDocument();
    expect(screen.getAllByText("Not chosen yet")).toHaveLength(50);
  });

  it("has no bulk control", () => {
    show(filled(120));
    const names = screen.getAllByRole("button").map((b) => b.textContent);
    expect(names.some((t) => /\b(all|every)\b/i.test(t ?? "") && !/without a choice/.test(t ?? ""))).toBe(false);
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });

  it("says 'of at least' while pages are still loading", () => {
    show(filled(120), { loading: true });
    expect(screen.getByText("Reasons 1 to 50 of at least 120")).toBeInTheDocument();
  });

  it("moves between display pages without choosing anything", async () => {
    const store = filled(120);
    show(store);
    await userEvent.click(screen.getByRole("button", { name: "Next 50" }));
    expect(screen.getByText("Reasons 51 to 100 of 120")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Reason 51 of 120" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Next 50" }));
    expect(screen.getByText("Reasons 101 to 120 of 120")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next 50" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Previous 50" }));
    expect(screen.getByText("Reasons 51 to 100 of 120")).toBeInTheDocument();
    expect(store.getSnapshot().unchosen).toBe(120);
  });

  it("jumps to a reason number, focuses its heading and says where it is", async () => {
    show(filled(120));
    await userEvent.type(screen.getByLabelText("Go to reason number"), "77");
    await userEvent.click(screen.getByRole("button", { name: "Go" }));
    expect(screen.getByText("Reasons 51 to 100 of 120")).toBeInTheDocument();
    expect(document.querySelector('[data-reason-heading="76"]')).toHaveFocus();
    expect(screen.getByText("Showing reasons 51 to 100 of 120. Reason 77 needs a choice.")).toBeInTheDocument();
  });

  it("refuses a jump outside the list and stays where it is", async () => {
    show(filled(120));
    await userEvent.type(screen.getByLabelText("Go to reason number"), "500");
    await userEvent.click(screen.getByRole("button", { name: "Go" }));
    expect(screen.getByText("Reasons 1 to 50 of 120")).toBeInTheDocument();
    expect(screen.getByText("Enter a reason number from 1 to 120.")).toBeInTheDocument();
  });

  it("goes to the next reason without a choice, then says every reason has one", async () => {
    const store = filled(120);
    for (let i = 0; i < 120; i++) if (i !== 64 && i !== 3) store.choose(i, KEEP);
    show(store);
    await userEvent.click(screen.getByRole("button", { name: "Next reason without a choice" }));
    expect(document.querySelector('[data-reason-heading="3"]')).toHaveFocus();
    await userEvent.click(within(screen.getByRole("group", { name: "Reason 4 of 120" })).getByRole("radio", { name: "Keep text" }));
    await userEvent.click(screen.getByRole("button", { name: "Next reason without a choice" }));
    expect(document.querySelector('[data-reason-heading="64"]')).toHaveFocus();
    expect(screen.getByText("Reasons 51 to 100 of 120")).toBeInTheDocument();
    act(() => store.choose(64, REMOVE));
    expect(screen.getByRole("button", { name: "Next reason without a choice" })).toBeDisabled();
  });

  it("offers the next changed reason only when one is open", async () => {
    const store = filled(60);
    store.reset(store.carry());
    store.appendPage(Array.from({ length: 60 }, (_, i) => item(i + 1)));
    show(store);
    expect(screen.getAllByText("Changed since you looked: choose again")).toHaveLength(50);
    await userEvent.click(screen.getByRole("button", { name: "Next 50" }));
    await userEvent.click(screen.getByRole("button", { name: "Next changed reason" }));
    expect(document.querySelector('[data-reason-heading="50"]')).toHaveFocus();
    act(() => {
      for (let i = 0; i < 60; i++) store.choose(i, KEEP);
    });
    expect(screen.queryByRole("button", { name: "Next changed reason" })).not.toBeInTheDocument();
  });

  it("re-renders one row for one click, and the DOM holds one display page whatever the length", async () => {
    const store = filled(2000);
    show(store);
    expect(document.querySelectorAll("li[data-reason-id]")).toHaveLength(DISPLAY_PAGE);
    rowRenders.n = 0;
    const group = screen.getByRole("group", { name: "Reason 7 of 2000" });
    await userEvent.click(within(group).getByRole("radio", { name: "Blank text" }));
    expect(within(group).getByRole("radio", { name: "Blank text" })).toBeChecked();
    expect(rowRenders.n).toBe(1);
    expect(store.choiceAt(6)).toBe(REMOVE);
  });

  it("a click commits the section, the progress line and the summary once each, and renders one row", async () => {
    const store = filled(2000);
    const commits: Record<string, number> = {};
    const count = (id: string) => {
      commits[id] = (commits[id] ?? 0) + 1;
    };
    render(
      <>
        <Profiler id="section" onRender={count}>
          <ReasonReviewSection store={store} disabled={false} loading={false} jump={null} />
        </Profiler>
        <Profiler id="progress" onRender={count}>
          <ReasonProgress store={store} done />
        </Profiler>
        <Profiler id="summary" onRender={count}>
          <ReasonSummary store={store} />
        </Profiler>
      </>,
    );
    for (const k of Object.keys(commits)) commits[k] = 0;
    rowRenders.n = 0;
    await userEvent.click(within(screen.getByRole("group", { name: "Reason 3 of 2000" })).getByRole("radio", { name: "Keep text" }));
    expect(commits).toEqual({ section: 1, progress: 1, summary: 1 });
    expect(rowRenders.n).toBe(1);
    await userEvent.click(within(screen.getByRole("group", { name: "Reason 9 of 2000" })).getByRole("radio", { name: "Blank text" }));
    expect(commits).toEqual({ section: 2, progress: 2, summary: 2 });
    expect(rowRenders.n).toBe(2);
  });

  it("disables every radio while a press is in flight", () => {
    show(filled(60), { disabled: true });
    for (const r of screen.getAllByRole("radio")) expect(r).toBeDisabled();
  });

  it("shows nothing for an empty store", () => {
    show(new ReasonReviewStore());
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
  });
});

describe("ReasonProgress and ReasonSummary", () => {
  it("says what is left in words, and the summary says what Delete would do", () => {
    const store = filled(3);
    show(store);
    expect(screen.getByText("Choose Keep text or Blank text for 3 more reasons.")).toBeInTheDocument();
    act(() => {
      store.choose(0, KEEP);
      store.choose(1, REMOVE);
      store.choose(2, REMOVE);
    });
    expect(screen.getByText("All 3 reasons chosen.")).toBeInTheDocument();
    expect(screen.getByText("Delete this account. Keep 1 reason and blank 2.")).toBeInTheDocument();
  });

  it("is told to assistive tech only after two quiet seconds, never once per click", () => {
    vi.useFakeTimers();
    try {
      const store = filled(3);
      render(<ReasonProgress store={store} done />);
      const live = () => screen.getAllByRole("status")[0].textContent;
      act(() => vi.advanceTimersByTime(2000));
      expect(live()).toBe("Choose Keep text or Blank text for 3 more reasons.");
      act(() => store.choose(0, KEEP));
      act(() => vi.advanceTimersByTime(500));
      act(() => store.choose(1, KEEP));
      act(() => vi.advanceTimersByTime(1500));
      expect(live()).toBe("Choose Keep text or Blank text for 3 more reasons."); // not yet
      act(() => vi.advanceTimersByTime(600));
      expect(live()).toBe("Choose Keep text or Blank text for 1 more reason.");
    } finally {
      vi.useRealTimers();
    }
  });
});

// The QC-1 harness drives the deletion panel in a real browser by the selectors, labels and copy in
// deploy/digitalocean/qc1/app-facts.json ("web"). These tests hold that file equal to what the components
// render. The users list and the panel's own copy are held by Admin.test.tsx and DeleteUserPanel.test.tsx.
const F = QC1_WEB;

const showWithProgress = (store: ReasonReviewStore, loading = false) =>
  render(
    <>
      <ReasonReviewSection store={store} disabled={false} loading={loading} jump={null} />
      <ReasonProgress store={store} done={!loading} />
    </>,
  );

describe("what the QC-1 harness drives in the reason review", () => {
  it("renders the reason rows the harness counts, each with its id and heading", () => {
    show(filled(120));
    const rows = document.querySelectorAll(F.reasonRows);
    expect(rows).toHaveLength(F.displayPage);
    expect(rows[0].getAttribute(F.reasonIdAttr)).toBe("1");
    expect(rows[0].querySelector(`[${F.reasonHeadingAttr}="0"]`)).not.toBeNull();
    // Nothing is preselected, and every row says so in the words the harness waits to disappear.
    for (const row of rows) expect(row.textContent).toContain(F.copy.notChosen);
  });

  it("renders the changed notice the harness waits on", () => {
    const store = filled(60);
    store.reset(store.carry());
    store.appendPage(Array.from({ length: 60 }, (_, i) => item(i + 1)));
    show(store);
    const rows = document.querySelectorAll(F.reasonRows);
    expect(rows[0].textContent).toContain(F.copy.changed);
  });

  it("renders the pager line, its loading form, and the controls the harness presses", () => {
    const { unmount } = showWithProgress(filled(120));
    expect(document.querySelector(F.reasonsNav)?.textContent).toBe(`Reasons 1 to ${F.displayPage} ${F.navOfDone}120`);
    expect(screen.getByRole("button", { name: `Next ${F.displayPage}` })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: `Previous ${F.displayPage}` })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: F.labels.nextUnchosen })).toBeInTheDocument();
    expect(screen.getByLabelText(F.labels.goToReason)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(`^${F.labels.goButton}$`) })).toBeInTheDocument();
    unmount();
    showWithProgress(filled(120), true);
    expect(document.querySelector(F.reasonsNav)?.textContent).toBe(`Reasons 1 to ${F.displayPage} ${F.navOfLoading}120`);
  });

  it("offers the changed-reason control by its label once one is open", () => {
    const store = filled(60);
    store.reset(store.carry());
    store.appendPage(Array.from({ length: 60 }, (_, i) => item(i + 1)));
    show(store);
    expect(screen.getByRole("button", { name: F.labels.nextChanged })).toBeInTheDocument();
  });

  it("gives every row the Keep and Blank radios the harness checks, and a remove value it can click", () => {
    show(filled(120));
    const row = document.querySelector(F.reasonRows) as HTMLElement;
    expect(within(row).getByRole("radio", { name: F.labels.keepText })).toBeInTheDocument();
    expect(within(row).getByRole("radio", { name: F.labels.blankText })).toBeInTheDocument();
    expect(row.querySelector(`input[type="radio"][value="${F.radioRemoveValue}"]`)).not.toBeNull();
  });

  it("shows the progress line where the harness looks for it, in the forms it recognizes", () => {
    const pattern = new RegExp(F.progressPattern);
    const store = filled(120);
    const { unmount } = showWithProgress(store, true);
    const progress = () => [...document.querySelectorAll(F.progressSelector)].map((p) => p.textContent ?? "").find((t) => pattern.test(t));
    expect(progress()).toMatch(/chosen of 120 so far\./);
    unmount();
    show(store);
    expect(progress()).toContain(`${F.progressChoose} 120 more reasons.`);
  });
});
