import { Navigate, useLocation } from "react-router-dom";
import { useAuth } from "../auth";

// The sign-in gate for pages that are only for signed-in people: signed out
// goes to Log in, which brings them back to the same address afterwards.
// App waits for the session check before routing, so `user` is settled here.
export function RequireUser({ children }: { children: React.ReactNode }) {
  const { user } = useAuth();
  const location = useLocation();
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />;
  return <>{children}</>;
}
