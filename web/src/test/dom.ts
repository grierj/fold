export function requiredElement<T extends Element>(element: Element | null, kind: { new(): T; name: string }): T {
  if (!(element instanceof kind)) throw new Error(`Expected ${kind.name}`);

  return element;
}
