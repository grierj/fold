import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { Auth as AuthApi, Profile, ensureCsrf } from "./api";
import { errorCode } from "./lib/errors";
import { browserTimeZone } from "./lib/when";
import { leaveWithoutAsking } from "./lib/unsavedChanges";
import { deviceForLogout, forgetDevice } from "./lib/push";
import type { User } from "./types";

// clearClientData removes everything the SPA keeps in browser storage.
export function clearClientData() {
  try {
    localStorage.clear();
    sessionStorage.clear();
  } catch {
    // Storage can be unavailable (privacy modes); nothing to clear then.
  }
}

interface AuthState {
  user: User | null;
  loading: boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
  isAdmin: boolean;
  // The person may write announcements: they lead an approved group, host
  // a church-wide event, or are an admin (M3.4c). Shows Messages in the nav.
  canCompose: boolean;
}

const AuthContext = createContext<AuthState>({
  user: null,
  loading: true,
  login: async () => {},
  logout: async () => {},
  refresh: async () => {},
  isAdmin: false,
  canCompose: false,
});

// withZone fills in a person's time zone from this browser the first time
// it's empty (DEC-58), so their emails read in their own time. After that
// only the profile page changes it. A failure keeps the church's zone.
async function withZone(user: User): Promise<User> {
  if (user.timezone || user.status === "pending_consent") return user;
  try {
    return (await Profile.update({ timezone: browserTimeZone() })).user;
  } catch {
    return user;
  }
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);
  const [canCompose, setCanCompose] = useState(false);

  const refresh = useCallback(async () => {
    try {
      await ensureCsrf();
      const { user, canCompose } = await AuthApi.me();
      setCanCompose(!!user && !!canCompose);
      setUser(user ? await withZone(user) : user);
    } catch {
      setCanCompose(false);
      setUser(null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const login = useCallback(async (email: string, password: string) => {
    try {
      const { user, canCompose } = await AuthApi.login(email, password);
      setCanCompose(!!canCompose);
      setUser(await withZone(user));
    } catch (error) {
      // A verified pending-teen attempt clears any previous server login.
      if (errorCode(error) === "consent_required") { setCanCompose(false); setUser(null); }
      throw error;
    }
  }, []);

  // Logout ends the server session, then drops every client-side copy of
  // the user's data. Pages keep what they fetched in component state and
  // don't refetch when the user changes, so clearing auth alone left the
  // previous screen up. A full load of "/" is the only reset that covers
  // everything (state, module caches like the CSRF token, history entries
  // that belonged to the old page). With notifications on, the request
  // names this browser's device so the server deletes it at once, and then
  // the browser drops its subscription and closes what's on screen (10.1).
  const logout = useCallback(async () => {
    const device = await deviceForLogout();
    try {
      await AuthApi.logout(device?.sub?.endpoint);
    } catch {
      // Still clear this browser; the reload shows if the server disagreed.
    }
    await forgetDevice(device);
    setUser(null);
    clearClientData();
    leaveWithoutAsking();
    window.location.replace("/");
  }, []);

  const value = useMemo(
    () => ({
      user,
      loading,
      login,
      logout,
      refresh,
      isAdmin: user?.role === "admin",
      canCompose: !!user && canCompose,
    }),
    [user, loading, login, logout, refresh, canCompose]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  return useContext(AuthContext);
}
