// Retained person IDs and audit cursors are exact decimal strings. Never
// coerce them to JS numbers; unrelated user-list IDs are still numeric.
export function retainedPersonId(value: string): string | null {
  if (!/^[0-9]+$/.test(value)) return null;
  const id = value.replace(/^0+/, "");
  if (!id || id.length > 19 || (id.length === 19 && id > "9223372036854775807")) return null;
  return id;
}
