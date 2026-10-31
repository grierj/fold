// ProseMirror measures the selection when it focuses or scrolls; jsdom has
// no layout, so give it empty rectangles (tests of the composer's editor).
export function polyfillEditorLayout() {
  const empty = () => Object.assign([], { item: () => null }) as unknown as DOMRectList;
  Range.prototype.getClientRects = empty;
  Range.prototype.getBoundingClientRect = () => new DOMRect();
  Element.prototype.getClientRects = empty;
  if (!document.elementFromPoint) document.elementFromPoint = () => null;
}
