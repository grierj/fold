import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { Auth as AuthApi, Profile, ensureCsrf } from "./api";
import { browserTimeZone } from "./lib/when";
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
}

const AuthContext = createContext<AuthState>({
  user: null,
  loading: true,
  login: async () => {},
  logout: async () => {},
  refresh: async () => {},
  isAdmin: false,
});

// withZone fills in a person's time zone from this browser the first time
// it's empty (DEC-58), so their emails read in their own time. After that
// only the profile page changes it. A failure keeps the church's zone.
async function withZone(user: User): Promise<User> {
  if (user.timezone) return user;
  try {
    return (await Profile.update({ timezone: browserTimeZone() })).user;
  } catch {
    return user;
  }
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    try {
      await ensureCsrf();
      const { user } = await AuthApi.me();
      setUser(user ? await withZone(user) : user);
    } catch {
      setUser(null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const login = useCallback(async (email: string, password: string) => {
    const { user } = await AuthApi.login(email, password);
    setUser(await withZone(user));
  }, []);

  // Logout ends the server session, then drops every client-side copy of
  // the user's data. Pages keep what they fetched in component state and
  // don't refetch when the user changes, so clearing auth alone left the
  // previous screen up. A full load of "/" is the only reset that covers
  // everything (state, module caches like the CSRF token, history entries
  // that belonged to the old page).
  const logout = useCallback(async () => {
    try {
      await AuthApi.logout();
    } catch {
      // Still clear this browser; the reload shows if the server disagreed.
    }
    setUser(null);
    clearClientData();
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
    }),
    [user, loading, login, logout, refresh]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  return useContext(AuthContext);
}
