import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { Field } from "../ui/Field";
import { polyfillEditorLayout } from "../../test/editor";
import { normalizeHref, RichTextEditor, RichTextView } from "./RichTextEditor";

beforeAll(polyfillEditorLayout);

function setup(value = "<p>Hello there</p>", oneEvent = false) {
  const onChange = vi.fn();
  const onReady = vi.fn();
  render(<Field label="Message" hint="Write it here." error={undefined}><RichTextEditor value={value} onChange={onChange} onReady={onReady} oneEvent={oneEvent} /></Field>);
  const box = screen.getByRole("textbox", { name: "Message" });
  return { onChange, onReady, box, last: () => onChange.mock.calls[onChange.mock.calls.length - 1]?.[0] as string };
}

async function selectAll(box: HTMLElement) {
  box.focus();
  await act(async () => { fireEvent.keyDown(box, { key: "a", code: "KeyA", ctrlKey: true }); });
}

describe("RichTextEditor", () => {
  it("is a labelled, described text box and reports its starting HTML", async () => {
    const { box, onReady } = setup();
    expect(box).toHaveAttribute("aria-multiline", "true");
    expect(box).toHaveAccessibleDescription("Write it here.");
    await waitFor(() => expect(onReady).toHaveBeenCalledWith("<p>Hello there</p>"));
  });

  it("has named toolbar buttons that show when a format is on", async () => {
    const { box, last } = setup();
    for (const name of ["Bold", "Italic", "Underline", "Heading H2", "Subheading H3", "Bulleted list", "Numbered list", "Quote", "Link", "Button"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    const bold = screen.getByRole("button", { name: "Bold" });
    expect(bold).toHaveAttribute("aria-pressed", "false");
    await selectAll(box);
    fireEvent.click(bold);
    await waitFor(() => expect(bold).toHaveAttribute("aria-pressed", "true"));
    expect(last()).toBe("<p><strong>Hello there</strong></p>");
    fireEvent.click(screen.getByRole("button", { name: "Heading H2" }));
    expect(last()).toMatch(/^<h2><strong>Hello there<\/strong><\/h2>/);
    fireEvent.click(screen.getByRole("button", { name: "Bulleted list" }));
    expect(last()).toContain("<ul><li>");
  });

  it("inserts merge fields, offering event fields only for one event's people", async () => {
    const first = setup("<p>Hi </p>");
    fireEvent.click(screen.getByRole("button", { name: "Insert field" }));
    expect(screen.getAllByRole("menuitem").map((m) => m.textContent)).toEqual(["First name"]);
    fireEvent.click(screen.getByRole("menuitem", { name: "First name" }));
    expect(first.last()).toContain("{{first_name}}");
    first.box.remove();
  });

  it("lists every merge field for an event's people", () => {
    setup("<p></p>", true);
    fireEvent.click(screen.getByRole("button", { name: "Insert field" }));
    expect(screen.getAllByRole("menuitem").map((m) => m.textContent)).toEqual(["First name", "Event title", "Event date and time", "Event page link", "Link to their sign-up"]);
  });

  it("adds a link and a button block the sanitizer keeps", async () => {
    const user = userEvent.setup();
    const { box, last } = setup();
    await selectAll(box);
    await user.click(screen.getByRole("button", { name: "Link" }));
    expect(screen.getByRole("button", { name: "Link" })).toHaveAttribute("aria-expanded", "true");
    await user.click(screen.getByRole("button", { name: "Add link" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Enter a web address.");
    await user.type(screen.getByRole("textbox", { name: "Web address" }), "example.org/news");
    await user.click(screen.getByRole("button", { name: "Add link" }));
    expect(last()).toBe('<p><a href="https://example.org/news">Hello there</a></p>');

    await user.click(screen.getByRole("button", { name: "Button" }));
    await user.type(screen.getByRole("textbox", { name: "Button words" }), "Sign up");
    await user.type(screen.getByRole("textbox", { name: "Web address" }), "https://example.org/go{Enter}");
    expect(last()).toContain('<a class="fold-button" href="https://example.org/go">Sign up</a>');
    expect(last()).not.toMatch(/target=|rel=/);
  });

  it("links a button to the event page for one event's people", async () => {
    const user = userEvent.setup();
    const { last } = setup("<p>Hi</p>", true);
    await user.click(screen.getByRole("button", { name: "Button" }));
    await user.type(screen.getByRole("textbox", { name: "Button words" }), "See the event");
    await user.selectOptions(screen.getByRole("combobox", { name: "The button opens" }), "The event's page");
    await user.click(screen.getByRole("button", { name: "Add button" }));
    expect(last()).toContain('<a class="fold-button" href="{{event_link}}">See the event</a>');
  });
});

describe("normalizeHref", () => {
  it("keeps only addresses the server keeps", () => {
    expect(normalizeHref("example.org")).toBe("https://example.org");
    expect(normalizeHref("http://example.org/x")).toBe("http://example.org/x");
    expect(normalizeHref("pastor@example.org")).toBe("mailto:pastor@example.org");
    expect(normalizeHref("{{ EVENT_LINK }}")).toBe("{{event_link}}");
    expect(normalizeHref("javascript:alert(1)")).toBe("");
    expect(normalizeHref("nonsense")).toBe("");
    expect(normalizeHref("  ")).toBe("");
  });
});

describe("RichTextView", () => {
  it("draws only the formatting the editor allows", () => {
    const { container } = render(<RichTextView html={'<p>Hi <strong>all</strong></p><img src="https://x.test/a.png"><script>alert(1)</script>'} />);
    expect(container.querySelector("strong")).toHaveTextContent("all");
    expect(container.querySelector("img, script")).toBeNull();
    expect(container.querySelector("[contenteditable=true]")).toBeNull();
  });
});
