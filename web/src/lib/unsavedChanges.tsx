import { useContext, useEffect, useRef, type ReactNode } from "react";
import { UNSAFE_DataRouterContext, useBlocker } from "react-router-dom";

// Leaving a page with unsaved edits asks first (DEC-98). Closing or reloading
// the tab gets the browser's own prompt; moving to another page in the app
// gets a confirm from the one app-wide <UnsavedChangesBlocker> (React Router
// consults only one blocker, so forms register here instead of each adding
// their own). Call `markSaved()` right before navigating away after a
// successful save, so the edits the save just stored don't block it.
const openForms = new Set<() => boolean>();
let leavingOnPurpose = false;

const anyUnsaved = () => !leavingOnPurpose && [...openForms].some((unsaved) => unsaved());

// Log out leaves the page on purpose; don't ask about unsaved edits then.
export function leaveWithoutAsking() {
  leavingOnPurpose = true;
}

export function useUnsavedChanges(dirty: boolean): { guard: ReactNode; markSaved: () => void } {
  const dirtyRef = useRef(dirty);
  const saved = useRef(false);
  dirtyRef.current = dirty;
  useEffect(() => {
    const unsaved = () => dirtyRef.current && !saved.current;
    openForms.add(unsaved);
    return () => { openForms.delete(unsaved); };
  }, []);
  useEffect(() => {
    if (!dirty) {
      saved.current = false;
      return;
    }
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      if (anyUnsaved()) e.preventDefault();
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [dirty]);
  // `guard` is kept for callers that render it; the app-wide blocker does the work.
  return { guard: null, markSaved: () => { saved.current = true; } };
}

// Rendered once inside the data router (App). useBlocker needs a data router;
// tests that render in a MemoryRouter get the tab-close prompt only.
export function UnsavedChangesBlocker() {
  const dataRouter = useContext(UNSAFE_DataRouterContext);
  return dataRouter ? <NavigationGuard /> : null;
}

function NavigationGuard() {
  const blocker = useBlocker(({ currentLocation, nextLocation }) =>
    anyUnsaved() && (currentLocation.pathname !== nextLocation.pathname || currentLocation.search !== nextLocation.search));
  useEffect(() => {
    if (blocker.state !== "blocked") return;
    if (window.confirm("Leave without saving? Your changes will be lost.")) blocker.proceed();
    else blocker.reset();
  }, [blocker]);
  return null;
}
