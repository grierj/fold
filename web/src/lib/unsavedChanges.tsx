import { useContext, useEffect, useRef, type ReactNode } from "react";
import { UNSAFE_DataRouterContext, useBlocker } from "react-router-dom";

// Leaving a page with unsaved edits asks first (DEC-98). Closing or reloading
// the tab gets the browser's own prompt; moving to another page in the app
// gets a confirm. Render `guard` somewhere in the form, and call `markSaved()`
// right before navigating away after a successful save, so that navigation
// isn't blocked by edits the save just stored.
export function useUnsavedChanges(dirty: boolean): { guard: ReactNode; markSaved: () => void } {
  const dirtyRef = useRef(dirty);
  const saved = useRef(false);
  dirtyRef.current = dirty;
  useEffect(() => {
    if (!dirty) {
      saved.current = false;
      return;
    }
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      if (!saved.current) e.preventDefault();
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [dirty]);
  // useBlocker needs a data router; tests that render in a MemoryRouter get
  // the tab-close prompt only.
  const dataRouter = useContext(UNSAFE_DataRouterContext);
  return {
    guard: dataRouter ? <NavigationGuard active={() => dirtyRef.current && !saved.current} /> : null,
    markSaved: () => { saved.current = true; },
  };
}

function NavigationGuard({ active }: { active: () => boolean }) {
  const blocker = useBlocker(({ currentLocation, nextLocation }) =>
    active() && (currentLocation.pathname !== nextLocation.pathname || currentLocation.search !== nextLocation.search));
  useEffect(() => {
    if (blocker.state !== "blocked") return;
    if (window.confirm("Leave without saving? Your changes will be lost.")) blocker.proceed();
    else blocker.reset();
  }, [blocker]);
  return null;
}
