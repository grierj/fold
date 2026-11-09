import { useCallback, useEffect, useSyncExternalStore } from "react";
import { Site } from "../api/site";
import type { SiteInfo } from "../types";

// The church's public settings (/site), read once and shared by every screen
// that asks, for the page's life. Only an answer is kept: a read that fails
// is not cached, so the next mount (or retry) asks again.
//
// One failure rule, decided per field by what is safe to assume when the
// answer is missing:
//   - rooms: off (no rooms control, no Rooms view);
//   - push: off (no notification controls, no setup page);
//   - self-signup: unknown (screens show an error with Try again rather than
//     a form the server may refuse, or an "On" the church may not have);
//   - church name: "" (the header says "Fold");
//   - church time zone: null (screens fall back to the browser's zone, or
//     wait on `done`, which is true after a failure too).
// A screen that can't proceed without the answer reads `error` and `retry`.

interface SiteState {
  site: SiteInfo | null;
  error: unknown;
  loading: boolean;
}

let state: SiteState = { site: null, error: null, loading: false };
let generation = 0;
const listeners = new Set<() => void>();

function set(next: SiteState) {
  state = next;
  listeners.forEach((l) => l());
}

function load() {
  const mine = ++generation;
  set({ ...state, error: null, loading: true });
  Site.get().then(
    (site) => { if (mine === generation) set({ site, error: null, loading: false }); },
    (error: unknown) => { if (mine === generation) set({ site: null, error, loading: false }); },
  );
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

// invalidateSite is called after an admin saves a setting that /site reports
// (time zone, self-signup, rooms, notifications), with the saved values as
// `saved` so screens show them at once. It then asks again in the background;
// with nothing on screen the next mount asks afresh.
export function invalidateSite(saved?: Partial<SiteInfo>) {
  if (saved && state.site) set({ ...state, site: { ...state.site, ...saved } });
  if (listeners.size > 0) load();
  else { generation++; set({ site: null, error: null, loading: false }); }
}

// forgetSite drops the shared answer without asking again, for tests.
export function forgetSite() {
  generation++;
  set({ site: null, error: null, loading: false });
}

export function useSite(): { site: SiteInfo | null; error: unknown; loading: boolean; retry: () => void } {
  const s = useSyncExternalStore(subscribe, () => state);
  // On mount only: a failed read is retried by the next mount or by retry(),
  // never by a loop.
  useEffect(() => {
    if (!state.site && !state.loading) load();
  }, []);
  const retry = useCallback(() => load(), []);
  // Loading means nothing to show yet: a refresh behind a shown answer is not loading.
  return { site: s.site, error: s.error, loading: !s.site && (s.loading || !s.error), retry };
}

// The church's name for display, or "" when it has none: "Fold" is the
// default, not a church's name.
export function churchName(site: { name: string } | null | undefined): string {
  return site && site.name && site.name !== "Fold" ? site.name : "";
}

// The church's time zone and whether /site has answered (done is true after
// a failure too, with zone null), for screens that must wait for the zone
// rather than briefly use the browser's.
export function useChurchZoneLoad(): { zone: string | null; done: boolean } {
  const { site, error } = useSite();
  return { zone: site?.timezone || null, done: !!site || !!error };
}

export function useChurchZone(): string | null {
  return useChurchZoneLoad().zone;
}

// The church's switch for notifications on devices (DEC-124). False until
// the answer is in, so a control that depends on it appears once instead of
// flashing and vanishing, and false when the read fails.
export function usePushEnabled(): boolean {
  const { site } = useSite();
  return !!site && site.pushEnabled !== false;
}

// Whether rooms are on: null until /site answers, false when it fails.
export function useRoomsEnabled(): boolean | null {
  const { site, error } = useSite();
  if (site) return site.roomsEnabled === true;
  return error ? false : null;
}
