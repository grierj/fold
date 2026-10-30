// In-page UI measurement shared by web/e2e/layout.spec.ts and
// .cursor/skills/verify-ui/drive-ui.mjs (ENFORCEMENT.md C2, C3). Pass it to
// page.evaluate. Returns horizontal overflow, selects whose chosen label is
// (and time zone comboboxes) wider than the room inside them (the PR #51
// clipped time zone), targets
// under 24x24 (WCAG 2.5.8; a <label for> or a wrapping <label> counts, inline links in running
// text are exempt) and targets under 44px tall (the design goal).
export const measure = () => {
  const vis = (e) => {
    const r = e.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== "hidden";
  };
  const name = (e) =>
    (e.getAttribute("aria-label") || e.textContent || e.getAttribute("name") || e.id || e.tagName)
      .trim().replace(/\s+/g, " ").slice(0, 40);
  const overflow = document.documentElement.scrollWidth - window.innerWidth;
  const ctx = document.createElement("canvas").getContext("2d");
  // The time zone combobox (DEC-65) counts too: at rest it shows the
  // current choice, which must never be cut off.
  const clipped = [...document.querySelectorAll("select, input[role=combobox]")].filter(vis).map((s) => {
    const cs = getComputedStyle(s);
    ctx.font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
    const label = s.tagName === "SELECT" ? s.options[s.selectedIndex]?.text ?? "" : s.value;
    const room = s.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
    return { id: s.id, label, text: Math.round(ctx.measureText(label).width), room: Math.round(room) };
  }).filter((x) => x.text > x.room);
  const inline = (e) =>
    e.tagName === "A" && [...e.parentElement.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim());
  const targets = [...document.querySelectorAll("a[href],button,input:not([type=hidden]),select,textarea,[role=button],[role=tab],summary")]
    .filter((e) => vis(e) && !inline(e) && !e.closest("[aria-hidden=true]"))
    .map((e) => {
      const r = e.getBoundingClientRect();
      const lab = (e.id && document.querySelector(`label[for="${e.id}"]`)) || e.closest("label");
      const lr = lab ? lab.getBoundingClientRect() : null;
      const h = Math.max(r.height, lr ? lr.height : 0), w = Math.max(r.width, lr ? lr.width : 0);
      return { el: e.tagName.toLowerCase() + (e.type ? `[${e.type}]` : ""), name: name(e), w: Math.round(w), h: Math.round(h) };
    });
  // Controls cut off by an ancestor that hides or scrolls its overflow, or
  // pushed past the viewport's sides. The page itself doesn't scroll
  // sideways in that case, so the overflow check alone misses it (PR #65
  // review F1: Admin Users actions inside overflow-hidden at 390).
  // A tab bar scrolls sideways inside its own bar on purpose (DEC-99); the
  // active tab is kept in view and a half-shown chip signals there is more.
  // So a tab is exempt only against that scroller (the nearest ancestor of its
  // tablist that scrolls sideways); any other ancestor still counts, and the
  // scroller itself is checked below like a control.
  const tabScroller = (e) => {
    if (!e.matches("[role=tab]")) return null;
    const list = e.closest("[role=tablist]");
    for (let a = list && list.parentElement; a && a !== document.body; a = a.parentElement) {
      if (/(auto|scroll)/.test(getComputedStyle(a).overflowX)) return a;
    }
    return null;
  };
  const controls = [...document.querySelectorAll("a[href],button,input:not([type=hidden]),select,textarea,[role=button],[role=tab],summary")]
    .filter((e) => vis(e) && !e.closest("[aria-hidden=true]"));
  const scrollers = new Set(controls.map(tabScroller).filter(Boolean));
  const cutOff = [...controls, ...scrollers]
    .filter((e) => {
      const own = tabScroller(e);
      const r = e.getBoundingClientRect();
      let left = r.left, right = r.right;
      // Only the part of a tab inside its scroller is on show.
      if (own) {
        const b = own.getBoundingClientRect();
        left = Math.max(left, b.left);
        right = Math.min(right, b.right);
      }
      if (left < -1 || right > window.innerWidth + 1) return true;
      for (let a = e.parentElement; a && a !== document.body; a = a.parentElement) {
        if (a === own) continue;
        const s = getComputedStyle(a);
        if (/(hidden|clip|auto|scroll)/.test(s.overflowX)) {
          const b = a.getBoundingClientRect();
          if (left < b.left - 1 || right > b.right + 1) return true;
        }
      }
      return false;
    })
    .map((e) => ({ el: e.tagName.toLowerCase(), name: name(e) }));
  return { overflow, clipped, cutOff, under24: targets.filter((t) => t.w < 24 || t.h < 24), under44: targets.filter((t) => t.h < 44) };
};
