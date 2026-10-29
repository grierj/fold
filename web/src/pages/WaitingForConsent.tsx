import { Navigate } from "react-router-dom";
import { useAuth } from "../auth";
import { ButtonLink } from "../components/ui/Button";

// This is a public explanation after password setup, not an authenticated
// holding screen. The shared shell keeps public groups and events available.
export default function WaitingForConsent() {
  const { user } = useAuth();
  if (user) return <Navigate to="/" replace />;
  return <div className="max-w-md mx-auto px-4 py-12 space-y-4">
    <h1 className="text-3xl font-bold">Your account is almost ready</h1>
    <p>Your account needs a parent or guardian's consent before you can log in. Your church office can help with the next step.</p>
    <p>You can still browse public groups and events without logging in.</p>
    <div className="flex flex-col sm:flex-row gap-3">
      <ButtonLink variant="primary" to="/calendar">Browse public events</ButtonLink>
      <ButtonLink variant="secondary" to="/login">Back to log in</ButtonLink>
    </div>
  </div>;
}
