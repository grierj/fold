import { lazy, Suspense, useEffect } from "react";
import { Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { Layout } from "./components/Layout";
import { Home } from "./pages/Home";
import { Login } from "./pages/Login";
import { GroupDetail } from "./pages/GroupDetail";
import { NewGroup } from "./pages/NewGroup";
import { NewEvent } from "./pages/NewEvent";
import { NewChurchEvent } from "./pages/NewChurchEvent";
import { EventDetail } from "./pages/EventDetail";
import { MyGroups } from "./pages/MyGroups";
import { ProfilePage } from "./pages/Profile";
import { Skeleton } from "./components/ui/Skeleton";
import { NotificationsPage } from "./pages/Notifications";
import { ForgotPassword } from "./pages/ForgotPassword";
import { ResetPassword } from "./pages/ResetPassword";
import { Invite } from "./pages/Invite";
import { VerifyEmail } from "./pages/VerifyEmail";
import { Join } from "./pages/Join";
import { SetPassword } from "./pages/SetPassword";
import { GuestConfirm } from "./pages/GuestConfirm";
import { GuestManage } from "./pages/GuestManage";
import { CalendarPage } from "./pages/Calendar";
import { NotFound } from "./pages/NotFound";
import { setOnUnauthorized } from "./api";
import { useAuth } from "./auth";

const AdminPage = lazy(() => import("./pages/Admin").then((module) => ({ default: module.AdminPage })));
const WaitingForConsent = lazy(() => import("./pages/WaitingForConsent"));
const GuardianConsentPage = lazy(() => import("./pages/GuardianConsent").then((m) => ({ default: m.GuardianConsentPage })));

export default function App() {
  const { user, loading } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();

  // The API returns 401 JSON (never redirects); the SPA owns routing, so any
  // unauthorized API call sends the user to /login, preserving their place.
  useEffect(() => {
    setOnUnauthorized(() => {
      if (location.pathname !== "/login") {
        navigate("/login", { state: { from: location.pathname }, replace: true });
      }
    });
    return () => setOnUnauthorized(null);
  }, [navigate, location.pathname]);

  if (loading) return null;
  if (user?.status === "pending_consent") return <Suspense fallback={null}><WaitingForConsent key={`${user.id}:${user.status}`} /></Suspense>;

  return (
    // Keyed by user: signing in as someone else remounts every page and the
    // header, so nothing fetched for the previous user (or while signed out)
    // survives. Routes wait for the session check so pages fetch once.
    <Layout key={user ? `${user.id}:${user.status}` : "anon"}>
      {loading ? null : (
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/login" element={<Login />} />
        <Route path="/forgot-password" element={<ForgotPassword />} />
        <Route path="/reset-password" element={<ResetPassword />} />
        <Route path="/invite" element={<Invite />} />
        <Route path="/join" element={<Join />} />
        <Route path="/set-password" element={<SetPassword />} />
        <Route path="/verify-email" element={<VerifyEmail />} />
        <Route path="/guardian-consent" element={<Suspense fallback={null}><GuardianConsentPage /></Suspense>} />
        <Route path="/guest/confirm" element={<GuestConfirm />} />
        <Route path="/guest/manage/:token" element={<GuestManage />} />
        <Route path="/groups/new" element={<NewGroup />} />
        <Route path="/groups/:id" element={<GroupDetail />} />
        <Route path="/groups/:id/events/new" element={<NewEvent />} />
        <Route path="/events/new" element={<NewChurchEvent />} />
        <Route path="/events/:id" element={<EventDetail />} />
        <Route path="/calendar" element={<CalendarPage />} />
        <Route path="/my-groups" element={<MyGroups />} />
        <Route path="/profile" element={<ProfilePage />} />
        <Route path="/admin" element={<Suspense fallback={<div className="max-w-6xl mx-auto px-4 py-8"><Skeleton kind="page" label="Loading admin…" /></div>}><AdminPage /></Suspense>} />
        <Route path="/notifications" element={<NotificationsPage />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
      )}
    </Layout>
  );
}
