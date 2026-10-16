// In-page UI measurement shared by web/e2e/layout.spec.ts and
// .cursor/skills/verify-ui/drive-ui.mjs (ENFORCEMENT.md C2, C3). Pass it to
// page.evaluate. Returns horizontal overflow, selects whose chosen label is
// wider than the room inside them (the PR #51 clipped time zone), targets
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
  const clipped = [...document.querySelectorAll("select")].filter(vis).map((s) => {
    const cs = getComputedStyle(s);
    ctx.font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
    const label = s.options[s.selectedIndex]?.text ?? "";
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
  const cutOff = [...document.querySelectorAll("a[href],button,input:not([type=hidden]),select,textarea,[role=button],[role=tab],summary")]
    .filter((e) => vis(e) && !e.closest("[aria-hidden=true]"))
    .filter((e) => {
      const r = e.getBoundingClientRect();
      if (r.left < -1 || r.right > window.innerWidth + 1) return true;
      for (let a = e.parentElement; a && a !== document.body; a = a.parentElement) {
        const s = getComputedStyle(a);
        if (/(hidden|clip|auto|scroll)/.test(s.overflowX)) {
          const b = a.getBoundingClientRect();
          if (r.left < b.left - 1 || r.right > b.right + 1) return true;
        }
      }
      return false;
    })
    .map((e) => ({ el: e.tagName.toLowerCase(), name: name(e) }));
  return { overflow, clipped, cutOff, under24: targets.filter((t) => t.w < 24 || t.h < 24), under44: targets.filter((t) => t.h < 44) };
};
