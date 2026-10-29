import { lazy, Suspense, useEffect } from "react";
import { Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { Layout } from "./components/Layout";
import { Home } from "./pages/Home";
import { Login } from "./pages/Login";
import { GroupDetail } from "./pages/GroupDetail";
import { EventDetail } from "./pages/EventDetail";
import { MyGroups } from "./pages/MyGroups";
import { Skeleton } from "./components/ui/Skeleton";
import { NotFound } from "./pages/NotFound";
import { setOnUnauthorized } from "./api";
import { useAuth } from "./auth";

const NewGroup = lazy(() => import("./pages/NewGroup").then((m) => ({ default: m.NewGroup })));
const NewEvent = lazy(() => import("./pages/NewEvent").then((m) => ({ default: m.NewEvent })));
const NewChurchEvent = lazy(() => import("./pages/NewChurchEvent").then((m) => ({ default: m.NewChurchEvent })));
const ProfilePage = lazy(() => import("./pages/Profile").then((m) => ({ default: m.ProfilePage })));
const NotificationsPage = lazy(() => import("./pages/Notifications").then((m) => ({ default: m.NotificationsPage })));
const CalendarPage = lazy(() => import("./pages/Calendar").then((m) => ({ default: m.CalendarPage })));
const ForgotPassword = lazy(() => import("./pages/ForgotPassword").then((m) => ({ default: m.ForgotPassword })));
const ResetPassword = lazy(() => import("./pages/ResetPassword").then((m) => ({ default: m.ResetPassword })));
const Invite = lazy(() => import("./pages/Invite").then((m) => ({ default: m.Invite })));
const VerifyEmail = lazy(() => import("./pages/VerifyEmail").then((m) => ({ default: m.VerifyEmail })));
const CancelEmailChange = lazy(() => import("./pages/CancelEmailChange").then((m) => ({ default: m.CancelEmailChange })));
const Join = lazy(() => import("./pages/Join").then((m) => ({ default: m.Join })));
const SetPassword = lazy(() => import("./pages/SetPassword").then((m) => ({ default: m.SetPassword })));
const GuestConfirm = lazy(() => import("./pages/GuestConfirm").then((m) => ({ default: m.GuestConfirm })));
const GuestManage = lazy(() => import("./pages/GuestManage").then((m) => ({ default: m.GuestManage })));
const GuardianConsentPage = lazy(() => import("./pages/GuardianConsent").then((m) => ({ default: m.GuardianConsentPage })));
const ContactHistoryPage = lazy(() => import("./pages/ContactHistory").then((m) => ({ default: m.ContactHistoryPage })));
const AdminPage = lazy(() => import("./pages/Admin").then((module) => ({ default: module.AdminPage })));
const ContactRequestsPage = lazy(() => import("./pages/ContactRequests").then((m) => ({ default: m.ContactRequestsPage })));
const AddRequestsPage = lazy(() => import("./pages/AddRequests").then((m) => ({ default: m.AddRequestsPage })));
const EventDatePage = lazy(() => import("./pages/EventDate").then((m) => ({ default: m.EventDatePage })));
const WaitingForConsent = lazy(() => import("./pages/WaitingForConsent"));

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

  return (
    // Keyed by user: signing in as someone else remounts every page and the
    // header, so nothing fetched for the previous user (or while signed out)
    // survives. Routes wait for the session check so pages fetch once.
    <Layout key={user ? `${user.id}:${user.status}` : "anon"}>
      {loading ? null : (
      <Suspense fallback={<Skeleton kind="page" label="Loading page…" />}>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/login" element={<Login />} />
        <Route path="/waiting-for-consent" element={<WaitingForConsent />} />
        <Route path="/forgot-password" element={<ForgotPassword />} />
        <Route path="/reset-password" element={<ResetPassword />} />
        <Route path="/invite" element={<Invite />} />
        <Route path="/join" element={<Join />} />
        <Route path="/set-password" element={<SetPassword />} />
        <Route path="/verify-email" element={<VerifyEmail />} />
        <Route path="/cancel-email-change" element={<CancelEmailChange />} />
        <Route path="/guardian-consent" element={<Suspense fallback={null}><GuardianConsentPage /></Suspense>} />
        <Route path="/guest/confirm" element={<GuestConfirm />} />
        <Route path="/guest/manage/:token" element={<GuestManage />} />
        <Route path="/groups/new" element={<NewGroup />} />
        <Route path="/groups/:id" element={<GroupDetail />} />
        <Route path="/groups/:id/events/new" element={<NewEvent />} />
        <Route path="/events/new" element={<NewChurchEvent />} />
        <Route path="/events/:id" element={<EventDetail />} />
        <Route path="/events/:id/dates/:date" element={<Suspense fallback={<Skeleton kind="page" label="Loading this date…" />}><EventDatePage /></Suspense>} />
        <Route path="/calendar" element={<CalendarPage />} />
        <Route path="/my-groups" element={<MyGroups />} />
        <Route path="/profile/:section?" element={<ProfilePage />} />
        <Route path="/add-requests" element={<Suspense fallback={<Skeleton kind="page" label="Loading add requests…" />}><AddRequestsPage /></Suspense>} />
        <Route path="/add-requests/:id" element={<Suspense fallback={<Skeleton kind="page" label="Loading add request…" />}><AddRequestsPage /></Suspense>} />
        <Route path="/contact-requests" element={<Suspense fallback={<Skeleton kind="page" label="Loading contact requests…" />}><ContactRequestsPage /></Suspense>} />
        <Route path="/contact-requests/:id" element={<Suspense fallback={<Skeleton kind="page" label="Loading contact request…" />}><ContactRequestsPage /></Suspense>} />
        <Route path="/admin/contact-history" element={<Suspense fallback={<Skeleton kind="page" label="Loading contact history…" />}><ContactHistoryPage /></Suspense>} />
        <Route path="/admin/users/:id/contact-history" element={<Suspense fallback={<Skeleton kind="page" label="Loading contact history…" />}><ContactHistoryPage /></Suspense>} />
        <Route path="/admin" element={<Suspense fallback={<div className="max-w-6xl mx-auto px-4 py-8"><Skeleton kind="page" label="Loading admin…" /></div>}><AdminPage /></Suspense>} />
        <Route path="/notifications" element={<NotificationsPage />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
      </Suspense>
      )}
    </Layout>
  );
}
