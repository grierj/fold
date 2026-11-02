import { createContext, useContext, useRef, type ReactNode, type RefObject } from "react";

// Only a return position, never fetched groups or personal data. The shell is
// keyed by the signed-in user, so signing out discards it. Nothing is persisted.
type Position = { query: string; groupId: number; offset: number };
const DirectoryPosition = createContext<RefObject<Position | null> | null>(null);

export function DirectoryPositionProvider({ children }: { children: ReactNode }) {
  const position = useRef<Position | null>(null);
  return <DirectoryPosition.Provider value={position}>{children}</DirectoryPosition.Provider>;
}

export function useDirectoryPosition() {
  return useContext(DirectoryPosition);
}
