import { useEffect } from "react";
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
import { AdminPage } from "./pages/Admin";
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

  return (
    // Keyed by user: signing in as someone else remounts every page and the
    // header, so nothing fetched for the previous user (or while signed out)
    // survives. Routes wait for the session check so pages fetch once.
    <Layout key={user?.id ?? "anon"}>
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
        <Route path="/admin" element={<AdminPage />} />
        <Route path="/notifications" element={<NotificationsPage />} />
        <Route path="*" element={<NotFound />} />
      </Routes>
      )}
    </Layout>
  );
}
