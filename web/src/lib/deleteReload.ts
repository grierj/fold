// Which deletes must not be pressed again until the users list has been loaded
// afresh (T07B-D4 R4-C08: after history_unavailable, and after restore_detected,
// "Delete stays off until reload"). The delete panel is disposable: closing it, opening another
// person's, or switching to Disable all drop its state, so the requirement is
// kept here, in memory only. It holds a person's id and a sequence number and
// never an attempt id or anything else that could act for one.
//
// A requirement ends only when a list load that began after it was recorded
// succeeds: a load already in flight when the answer arrived may predate the
// delete, and a failed load proves nothing.

let seq = 0;
const pending = new Map<number, number>();
// restore_detected is about the whole server, not one person: after it every
// delete waits for a fresh list, with its own wording (0 means none).
let restoreAt = 0;

export function requireReload(userId: number): void {
  pending.set(userId, ++seq);
}

export function needsReload(userId: number): boolean {
  return pending.has(userId);
}

export function requireRestoreReload(): void {
  restoreAt = ++seq;
}

export function needsRestoreReload(): boolean {
  return restoreAt !== 0;
}

// Call as a list load begins; pass the result to reloadSucceeded if it works.
export function reloadStarted(): number {
  return seq;
}

export function reloadSucceeded(started: number): void {
  for (const [userId, at] of pending) if (at <= started) pending.delete(userId);
  if (restoreAt !== 0 && restoreAt <= started) restoreAt = 0;
}
