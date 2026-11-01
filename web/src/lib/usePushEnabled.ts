import { useEffect, useState } from "react";
import { Site } from "../api";

// One /site read shared by every form that asks, for the page's life: a
// read that fails is dropped, so the next mount asks again.
let shared: Promise<boolean> | null = null;
let known: boolean | null = null;

function pushEnabled(): Promise<boolean> {
  if (!shared) {
    shared = Site.get().then(
      (s) => (known = s.pushEnabled !== false),
      () => {
        shared = null;
        return true;
      },
    );
  }
  return shared;
}

// usePushEnabled is the church's switch for notifications on devices
// (DEC-124) from /site. Until the first answer it is false, so a control
// that depends on it appears once rather than flashing and then vanishing
// for a church with push off; after that every mount knows at once. A
// failed read turns it on; the server still decides what pushes.
export function usePushEnabled(): boolean {
  const [on, setOn] = useState(known ?? false);
  useEffect(() => {
    let alive = true;
    void pushEnabled().then((v) => { if (alive) setOn(v); });
    return () => { alive = false; };
  }, []);
  return on;
}

// forgetPushEnabled drops the shared answer, for tests that change /site.
export function forgetPushEnabled() {
  shared = null;
  known = null;
}
