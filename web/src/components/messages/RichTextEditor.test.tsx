import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../api/core";
import { Messages } from "../../api/messages";
import { Field } from "../ui/Field";
import { polyfillEditorLayout } from "../../test/editor";
import { normalizeHref, RichTextEditor, RichTextView, type PictureTarget } from "./RichTextEditor";

vi.mock("../../api/messages", () => ({ Messages: { uploadImage: vi.fn() } }));

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
    // The empty paragraph the editor keeps after a heading is left out.
    expect(last()).toBe("<h2><strong>Hello there</strong></h2>");
    fireEvent.click(screen.getByRole("button", { name: "Bulleted list" }));
    expect(last()).toContain("<ul><li>");
  });

  it("names every toolbar button with its visible text in the name (WCAG 2.5.3)", () => {
    setup();
    const toolbar = screen.getByRole("group", { name: "Formatting" });
    for (const b of within(toolbar).getAllByRole("button")) {
      const visible = [...b.childNodes].filter((n) => !(n instanceof Element && n.getAttribute("aria-hidden") === "true"))
        .map((n) => n.textContent ?? "").join("").replace(/\s+/g, " ").trim().toLowerCase();
      const name = (b.getAttribute("aria-label") ?? b.textContent ?? "").toLowerCase();
      expect(name, b.outerHTML).toContain(visible);
    }
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

function setupPictures(pictures: PictureTarget | undefined) {
  const onChange = vi.fn();
  render(<Field label="Message"><RichTextEditor value="<p>Hello there</p>" onChange={onChange} oneEvent={false} pictures={pictures} /></Field>);
  return { last: () => onChange.mock.calls[onChange.mock.calls.length - 1]?.[0] as string };
}

const picnic = () => new File(["png"], "picnic.png", { type: "image/png" });
const fileInput = () => document.querySelector<HTMLInputElement>('input[type="file"]')!;

describe("RichTextEditor pictures", () => {
  it("has no Add image button unless the page can add pictures", () => {
    setupPictures(undefined);
    expect(screen.queryByRole("button", { name: "Add image" })).toBeNull();
  });

  it("asks what the picture shows, uploads it to the message and puts it in the body", async () => {
    const user = userEvent.setup();
    vi.mocked(Messages.uploadImage).mockResolvedValue({ key: "messages/5/aa.png", url: "/uploads/messages/5/aa.png" });
    const save = vi.fn();
    const { last } = setupPictures({ messageId: 5, save });
    await user.click(screen.getByRole("button", { name: "Add image" }));
    expect(screen.getByRole("group", { name: "Add a picture" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "What the picture shows" })).toHaveAccessibleDescription(/A few words for people who can't see it/);
    await user.click(screen.getByRole("button", { name: "Add picture" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Choose a picture first.");
    await user.upload(fileInput(), picnic());
    expect(screen.getByText("picnic.png")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Add picture" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Say what the picture shows, in a few words.");
    expect(Messages.uploadImage).not.toHaveBeenCalled();
    await user.type(screen.getByRole("textbox", { name: "What the picture shows" }), "  Our picnic table  ");
    await user.click(screen.getByRole("button", { name: "Add picture" }));
    await waitFor(() => expect(Messages.uploadImage).toHaveBeenCalledWith(5, expect.any(File)));
    await waitFor(() => expect(last()).toMatch(/<img[^>]*src="\/uploads\/messages\/5\/aa\.png"[^>]*alt="Our picnic table"/));
    expect(screen.queryByRole("group", { name: "Add a picture" })).toBeNull();
    expect(save).not.toHaveBeenCalled();
  });

  it("saves a new message as a draft first, and says why when it can't", async () => {
    const user = userEvent.setup();
    vi.mocked(Messages.uploadImage).mockResolvedValue({ key: "messages/9/bb.png", url: "/uploads/messages/9/bb.png" });
    const save = vi.fn().mockResolvedValueOnce({ error: "Choose who this message is for first." }).mockResolvedValueOnce({ id: 9 });
    const { last } = setupPictures({ messageId: null, save });
    await user.click(screen.getByRole("button", { name: "Add image" }));
    await user.upload(fileInput(), picnic());
    await user.type(screen.getByRole("textbox", { name: "What the picture shows" }), "Table");
    await user.click(screen.getByRole("button", { name: "Add picture" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Choose who this message is for first.");
    expect(Messages.uploadImage).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Add picture" }));
    await waitFor(() => expect(Messages.uploadImage).toHaveBeenCalledWith(9, expect.any(File)));
    await waitFor(() => expect(last()).toContain('src="/uploads/messages/9/bb.png"'));
  });

  it("keeps the panel open with the server's refusal in words", async () => {
    const user = userEvent.setup();
    vi.mocked(Messages.uploadImage).mockRejectedValue(new ApiError(400, "file too large", { code: "file_too_large" }));
    const { last } = setupPictures({ messageId: 5, save: vi.fn() });
    await user.click(screen.getByRole("button", { name: "Add image" }));
    await user.upload(fileInput(), picnic());
    await user.type(screen.getByRole("textbox", { name: "What the picture shows" }), "Table{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("That picture is too big. Please use one under 5 MB.");
    expect(screen.getByRole("group", { name: "Add a picture" })).toBeInTheDocument();
    expect(last()).toBeUndefined();
  });

  it("turns down a file that isn't a JPEG, PNG or GIF before uploading it", async () => {
    const user = userEvent.setup();
    setupPictures({ messageId: 5, save: vi.fn() });
    await user.click(screen.getByRole("button", { name: "Add image" }));
    fireEvent.change(fileInput(), { target: { files: [new File(["x"], "notes.pdf", { type: "application/pdf" })] } });
    expect(screen.getByRole("alert")).toHaveTextContent("Choose a JPEG, PNG or GIF picture.");
    expect(Messages.uploadImage).not.toHaveBeenCalled();
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
  it("shows merge-field links as text, with a note that they fill in when sent", () => {
    const { container } = render(<RichTextView html={'<p><a href="{{event_link}}" class="fold-button">See it</a> and <a href="https://example.org">this</a></p>'} />);
    expect(container).toHaveTextContent("See it and this");
    expect(container.querySelector("a[href*=\"event_link\"]")).toBeNull();
    expect(container.querySelector('a[href="https://example.org"]')).toHaveTextContent("this");
    expect(screen.getByText(/fill in when it's sent/)).toBeInTheDocument();
  });

  it("shows the message's own pictures with their description, and no other image", () => {
    const { container } = render(<RichTextView html={'<img src="/uploads/messages/5/aa.png" alt="Our picnic table"><img src="/uploads/avatars/1/x.png" alt="No"><img src="https://x.test/b.png" alt="No">'} />);
    const pics = container.querySelectorAll("img");
    expect(pics).toHaveLength(1);
    expect(pics[0]).toHaveAttribute("src", "/uploads/messages/5/aa.png");
    expect(pics[0]).toHaveAttribute("alt", "Our picnic table");
  });

  it("draws only the formatting the editor allows", () => {
    const { container } = render(<RichTextView html={'<p>Hi <strong>all</strong></p><img src="https://x.test/a.png"><script>alert(1)</script>'} />);
    expect(container.querySelector("strong")).toHaveTextContent("all");
    expect(container.querySelector("img, script")).toBeNull();
    expect(container.querySelector("[contenteditable=true]")).toBeNull();
  });
});
