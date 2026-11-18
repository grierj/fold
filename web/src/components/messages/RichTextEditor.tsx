import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { EditorContent, getMarkRange, useEditor, useEditorState, type Editor } from "@tiptap/react";
import Image from "@tiptap/extension-image";
import StarterKit from "@tiptap/starter-kit";
import { Messages } from "../../api/messages";
import { errorText } from "../../lib/errors";
import { prepareUpload } from "../../lib/image";
import { Field, FormError, HiddenFileInput, Input, Select, useField } from "../ui/Field";
import { Button } from "../ui/Button";
import { Menu } from "../ui/Menu";
import { ToolbarButton } from "../ui/ToolbarButton";
import { ListIcon, ListOrderedIcon } from "../icons";
import { MERGE_FIELDS } from "../../lib/messages";

// The composer's email editor (M3.4c, PLAN 7.3): TipTap with bold, italic,
// underline, two heading sizes, lists, quotes, links, a button block,
// pictures and merge fields. It emits only what the server's sanitizer keeps
// (internal/content): p, br, strong, em, u, s, h2, h3, ul, ol, li,
// blockquote, hr, links (http, https, mailto, or a link merge field) with
// class="fold-button" for a button, and img whose address the composer's own
// upload made (DEC-118), each with a description. TipTap's own CSS is off
// (it would be an inline <style>, which the CSP refuses); index.css styles
// .rich-text instead. This module is loaded with import(), so pages that
// don't write email never download TipTap.

const BUTTON_CLASS = "fold-button";

// The longest description the server keeps (content.MaxAltLen).
const MAX_ALT = 300;
const LINK_FIELDS = ["{{event_link}}", "{{manage_link}}"];
const LINK_FIELD_RE = /^\{\{\s*(event_link|manage_link)\s*\}\}$/i;

// allowedHref: the addresses the server keeps.
function allowedHref(href: string): boolean {
  return /^(https?:\/\/|mailto:)/i.test(href.trim()) || LINK_FIELD_RE.test(href.trim());
}

// One set of extensions for every editor, so re-renders never rebuild it.
const kit = (isAllowedUri: (url: string) => boolean) => StarterKit.configure({
  code: false,
  codeBlock: false,
  heading: { levels: [2, 3] },
  link: {
    openOnClick: false,
    autolink: true,
    HTMLAttributes: { target: null, rel: null, class: null },
    isAllowedUri: (url) => isAllowedUri(url),
  },
});

// A picture is a block of its own, shrunk to the column. Only the composer's
// own uploads are read in (pasted or loaded HTML with any other image loses
// it, as the server's sanitizer would). Its class is the editor's styling; the
// sanitizer drops it from what is stored.
const picture = Image.extend({ parseHTML: () => [{ tag: 'img[src^="/uploads/messages/"]' }] }).configure({ inline: false, allowBase64: false, HTMLAttributes: { class: "max-w-full h-auto" } });
const extensions = [kit(allowedHref), picture];

// The read-only view keeps only real addresses: a link to a merge field
// ({{event_link}}) has no address until it is sent, so it reads as text.
const viewExtensions = [kit((url) => allowedHref(url) && !LINK_FIELD_RE.test(url.trim())), picture];

// normalizeHref turns what someone typed into an address the server keeps,
// or "" when it can't be one.
/** @public tested directly: which typed addresses are kept. */
export function normalizeHref(typed: string): string {
  const s = typed.trim();

  if (!s) return "";

  if (LINK_FIELD_RE.test(s)) return s.toLowerCase().replace(/\s+/g, "");

  if (/^mailto:/i.test(s)) return s;

  if (/^[^\s@/]+@[^\s@/]+\.[^\s@/]+$/.test(s)) return `mailto:${s}`;
  const withScheme = /^https?:\/\//i.test(s) ? s : /^[a-z][a-z0-9+.-]*:/i.test(s) ? "" : `https://${s}`;

  if (!withScheme) return "";

  try {
    const u = new URL(withScheme);

    return u.hostname.includes(".") || u.hostname === "localhost" ? withScheme : "";
  } catch {
    return "";
  }
}

type Panel = null | "link" | "button" | "image";

// What the editor needs to add a picture to a message: its id once saved,
// and a way to save a new one first (a picture belongs to a saved message).
export interface PictureTarget {
  messageId: number | null;
  save: () => Promise<{ id: number } | { error: string }>;
}

function Toolbar({ editor, oneEvent, panel, onPanel, pictures }: { editor: Editor; oneEvent: boolean; panel: Panel; onPanel: (p: Panel) => void; pictures: boolean }) {
  const on = useEditorState({
    editor,
    selector: ({ editor: e }) => ({
      bold: e.isActive("bold"),
      italic: e.isActive("italic"),
      underline: e.isActive("underline"),
      h2: e.isActive("heading", { level: 2 }),
      h3: e.isActive("heading", { level: 3 }),
      bullet: e.isActive("bulletList"),
      ordered: e.isActive("orderedList"),
      quote: e.isActive("blockquote"),
      link: e.isActive("link") && e.getAttributes("link").class !== BUTTON_CLASS,
      button: e.isActive("link") && e.getAttributes("link").class === BUTTON_CLASS,
    }),
  });

  const run = (f: (c: ReturnType<Editor["chain"]>) => ReturnType<Editor["chain"]>) => f(editor.chain().focus()).run();
  const fields = MERGE_FIELDS.filter((f) => oneEvent || !f.event);

  return (
    <div role="group" aria-label="Formatting" className="flex flex-wrap items-center gap-1 border-b border-border p-1">
      <ToolbarButton label="Bold" pressed={on.bold} onClick={() => run((c) => c.toggleBold())}><span className="font-bold">B</span></ToolbarButton>
      <ToolbarButton label="Italic" pressed={on.italic} onClick={() => run((c) => c.toggleItalic())}><span className="italic">I</span></ToolbarButton>
      <ToolbarButton label="Underline" pressed={on.underline} onClick={() => run((c) => c.toggleUnderline())}><span className="underline">U</span></ToolbarButton>
      <ToolbarButton label="Heading H2" pressed={on.h2} onClick={() => run((c) => c.toggleHeading({ level: 2 }))}>H2</ToolbarButton>
      <ToolbarButton label="Subheading H3" pressed={on.h3} onClick={() => run((c) => c.toggleHeading({ level: 3 }))}>H3</ToolbarButton>
      <ToolbarButton label="Bulleted list" pressed={on.bullet} onClick={() => run((c) => c.toggleBulletList())}><ListIcon className="h-5 w-5" /></ToolbarButton>
      <ToolbarButton label="Numbered list" pressed={on.ordered} onClick={() => run((c) => c.toggleOrderedList())}><ListOrderedIcon className="h-5 w-5" /></ToolbarButton>
      <ToolbarButton label="Quote" pressed={on.quote} onClick={() => run((c) => c.toggleBlockquote())}><span aria-hidden="true">“</span>&nbsp;Quote</ToolbarButton>
      <ToolbarButton label="Link" pressed={on.link} aria-expanded={panel === "link"} onClick={() => onPanel(panel === "link" ? null : "link")}>Link</ToolbarButton>
      <ToolbarButton label="Button" pressed={on.button} aria-expanded={panel === "button"} onClick={() => onPanel(panel === "button" ? null : "button")}>Button</ToolbarButton>
      {pictures && <ToolbarButton label="Add image" aria-expanded={panel === "image"} onClick={() => onPanel(panel === "image" ? null : "image")}>Image</ToolbarButton>}
      <Menu
        tone="quiet"
        text="Insert field"
        label="Insert field"
        align="start"
        items={fields.map((f) => ({ label: f.label, onSelect: () => run((c) => c.insertContent(`{{${f.field}}}`)) }))}
      />
    </div>
  );
}

function LinkPanel({ editor, kind, oneEvent, onClose }: { editor: Editor; kind: "link" | "button"; oneEvent: boolean; onClose: () => void }) {
  const current = editor.isActive("link") ? (editor.getAttributes("link")) : null;
  const editing = !!current && (current.class === BUTTON_CLASS) === (kind === "button");
  const startHref = editing ? current?.href ?? "" : "";
  const [target, setTarget] = useState(LINK_FIELDS.includes(startHref) && oneEvent ? startHref : "web");
  const [href, setHref] = useState(LINK_FIELDS.includes(startHref) ? "" : startHref);

  const [text, setText] = useState(() => {
    if (kind !== "button" || !editing) return "";
    const range = getMarkRange(editor.state.selection.$from, editor.schema.marks.link);

    return range ? editor.state.doc.textBetween(range.from, range.to) : "";
  });

  const [error, setError] = useState("");
  const first = useRef<HTMLInputElement>(null);
  useEffect(() => { first.current?.focus(); }, []);

  function apply() {
    const address = target === "web" ? normalizeHref(href) : target;

    if (kind === "button" && !text.trim()) {
      setError("Enter the words on the button.");

      return;
    }

    if (!address) {
      setError(href.trim() ? "Enter a web address that starts with https://, or an email address." : "Enter a web address.");

      return;
    }

    const chain = editor.chain().focus();

    if (kind === "button") {
      const mark = { type: "link", attrs: { href: address, class: BUTTON_CLASS } };

      if (editing) chain.extendMarkRange("link").insertContent({ type: "text", text: text.trim(), marks: [mark] }).run();
      else chain.insertContent({ type: "paragraph", content: [{ type: "text", text: text.trim(), marks: [mark] }] }).run();
    } else if (editor.state.selection.empty && !editing) {
      chain.insertContent({ type: "text", text: target === "web" ? href.trim() : target === LINK_FIELDS[0] ? "the event page" : "your sign-up", marks: [{ type: "link", attrs: { href: address } }] }).run();
    } else {
      chain.extendMarkRange("link").setLink({ href: address, class: null }).run();
    }

    onClose();
  }

  function remove() {
    editor.chain().focus().extendMarkRange("link").unsetLink().run();
    onClose();
  }

  // Enter applies the panel instead of submitting the whole message.
  function onKeyDown(e: KeyboardEvent) {
    if (e.key === "Enter") { e.preventDefault(); apply(); }

    if (e.key === "Escape") { e.preventDefault(); onClose(); editor.commands.focus(); }
  }

  const noun = kind === "button" ? "button" : "link";

  return (
    <div role="group" aria-label={editing ? `Change ${noun}` : `Add a ${noun}`} className="space-y-3 border-b border-border bg-sunken p-3" onKeyDown={onKeyDown}>
      {kind === "button" && (
        <Field label="Button words" hint="A few words, like “Sign up”.">
          <Input ref={first} value={text} maxLength={80} onChange={(e) => { setText(e.target.value); setError(""); }} />
        </Field>
      )}
      {oneEvent && (
        <Field label={kind === "button" ? "The button opens" : "Link to"}>
          <Select value={target} onChange={(e) => { setTarget(e.target.value); setError(""); }} options={[
            { value: "web", label: "A web address" },
            { value: LINK_FIELDS[0], label: "The event's page" },
            { value: LINK_FIELDS[1], label: "Their sign-up" },
          ]} />
        </Field>
      )}
      {target === "web" && (
        <Field label="Web address" hint="Starts with https://. An email address works too.">
          <Input ref={kind === "link" ? first : undefined} value={href} inputMode="url" autoComplete="url" onChange={(e) => { setHref(e.target.value); setError(""); }} />
        </Field>
      )}
      {error && <FormError>{error}</FormError>}
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" size="sm" onClick={apply}>{editing ? `Change ${noun}` : `Add ${noun}`}</Button>
        {editing && <Button variant="ghost" size="sm" onClick={remove}>Remove {noun}</Button>}
        <Button variant="ghost" size="sm" onClick={() => { onClose(); editor.commands.focus(); }}>Cancel</Button>
      </div>
    </div>
  );
}

const PICTURE_TYPES = /^image\/(jpeg|png|gif)$/;

// ImagePanel adds one picture: choose it, say what it shows (the words email
// and screen readers use; required), and it is uploaded to the message and
// put in the body where the cursor is.
function ImagePanel({ editor, pictures, onClose }: { editor: Editor; pictures: PictureTarget; onClose: () => void }) {
  const [file, setFile] = useState<File | null>(null);
  const [alt, setAlt] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const chooser = useRef<HTMLInputElement>(null);
  const words = useRef<HTMLInputElement>(null);

  function pick(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    e.target.value = "";

    if (!f) return;

    if (!PICTURE_TYPES.test(f.type)) {
      setFile(null);
      setError("Choose a JPEG, PNG or GIF picture.");

      return;
    }

    setFile(f);
    setError("");
    words.current?.focus();
  }

  async function apply() {
    if (busy) return;

    if (!file) {
      setError("Choose a picture first.");

      return;
    }

    if (!alt.trim()) {
      setError("Say what the picture shows, in a few words.");
      words.current?.focus();

      return;
    }

    setBusy(true);
    setError("");

    try {
      let id = pictures.messageId;

      if (id === null) {
        const saved = await pictures.save();

        if ("error" in saved) {
          setError(saved.error);

          return;
        }

        id = saved.id;
      }

      const { url } = await Messages.uploadImage(id, await prepareUpload(file, 1600));
      editor.chain().focus().setImage({ src: url, alt: alt.trim() }).run();
      onClose();
    } catch (e) {
      setError(errorText(e, "We couldn't add that picture."));
    } finally {
      setBusy(false);
    }
  }

  // Enter in the description adds the picture instead of sending the message.
  function onKeyDown(e: KeyboardEvent) {
    if (e.key === "Enter" && e.target instanceof HTMLInputElement) { e.preventDefault(); void apply(); }

    if (e.key === "Escape") { e.preventDefault(); onClose(); editor.commands.focus(); }
  }

  return (
    <div role="group" aria-label="Add a picture" className="space-y-3 border-b border-border bg-sunken p-3" onKeyDown={onKeyDown}>
      <div className="space-y-1">
        <p className="label">Picture</p>
        <p className="field-hint">A JPEG, PNG or GIF under 5 MB. It goes in the email as a link to your church's site.</p>
        <div className="flex flex-wrap items-center gap-3">
          <Button variant="secondary" size="sm" onClick={() => chooser.current?.click()} disabled={busy}>{file ? "Choose a different picture" : "Choose a picture"}</Button>
          <span className="text-sm text-text-muted wrap-anywhere">{file ? file.name : "No picture chosen yet."}</span>
        </div>
        <HiddenFileInput ref={chooser} accept="image/jpeg,image/png,image/gif" onChange={pick} />
      </div>
      <Field label="What the picture shows" hint="A few words for people who can't see it. It also shows if the picture doesn't load.">
        <Input ref={words} value={alt} maxLength={MAX_ALT} onChange={(e) => { setAlt(e.target.value); setError(""); }} />
      </Field>
      {error && <FormError>{error}</FormError>}
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" size="sm" onClick={() => void apply()} disabled={busy}>{busy ? "Adding…" : "Add picture"}</Button>
        <Button variant="ghost" size="sm" onClick={() => { onClose(); editor.commands.focus(); }} disabled={busy}>Cancel</Button>
      </div>
    </div>
  );
}

// htmlOf is what the editor holds, or "" when it holds nothing.
// Empty paragraphs at the end (the editor keeps one to type into) are left out.
const htmlOf = (e: Editor) => (e.isEmpty ? "" : e.getHTML().replace(/(<p><\/p>)+$/, ""));

// RichTextEditor sits inside a <Field>, which labels it and carries its
// hint and error. onChange gets the HTML ("" when empty); onReady gets the
// starting HTML as the editor writes it, so a page can compare edits to it.
export function RichTextEditor({ value, onChange, onReady, oneEvent, pictures }: {
  value: string;
  onChange: (html: string) => void;
  onReady?: (html: string) => void;
  oneEvent: boolean;
  // Left out, there is no Add image button.
  pictures?: PictureTarget;
}) {
  const w = useField("RichTextEditor");
  const [panel, setPanel] = useState<Panel>(null);
  const changed = useRef(onChange);
  changed.current = onChange;
  const ready = useRef(onReady);
  ready.current = onReady;

  const editorProps = useMemo(() => {
    const attributes: Partial<Record<"aria-describedby" | "aria-invalid", string>> = {};

    if (w.describedBy) attributes["aria-describedby"] = w.describedBy;

    if (w.invalid) attributes["aria-invalid"] = "true";

    return { attributes: {
      id: w.id,
      role: "textbox",
      "aria-multiline": "true",
      "aria-labelledby": w.labelId,
      ...attributes,
      class: "rich-text rich-text-editable",
    } };
  }, [w.id, w.labelId, w.describedBy, w.invalid]);

  const editor = useEditor({
    extensions,
    content: value,
    injectCSS: false,
    editorProps,
    onCreate: ({ editor: e }) => ready.current?.(htmlOf(e)),
    onUpdate: ({ editor: e }) => changed.current(htmlOf(e)),
  });

  if (!editor) return null;

  return (
    <div className="rounded-md border border-border-strong bg-surface">
      <Toolbar editor={editor} oneEvent={oneEvent} panel={panel} onPanel={setPanel} pictures={!!pictures} />
      {panel === "image" && pictures && <ImagePanel key="image" editor={editor} pictures={pictures} onClose={() => setPanel(null)} />}
      {(panel === "link" || panel === "button") && <LinkPanel key={panel} editor={editor} kind={panel} oneEvent={oneEvent} onClose={() => setPanel(null)} />}
      <EditorContent editor={editor} />
    </div>
  );
}

// RichTextView shows a saved message's body, read only. It goes through the
// same schema as the editor, so only the formatting it allows is drawn.
export function RichTextView({ html }: { html: string }) {
  const editor = useEditor({
    extensions: viewExtensions,
    content: html,
    editable: false,
    injectCSS: false,
    editorProps: { attributes: { class: "rich-text", role: "region", "aria-label": "Message body" } },
  });

  if (!editor) return null;

  return <>
    {/(href="\{\{\s*(event_link|manage_link)\s*\}\}")/i.test(html) && (
      <p className="mb-3 text-sm text-text-muted">Links to the event page and to each person's sign-up fill in when it's sent.</p>
    )}
    <EditorContent editor={editor} />
  </>;
}
