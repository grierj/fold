import { useEffect } from "react";
import { Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { Layout } from "./components/Layout";
import { Home } from "./pages/Home";
import { Login } from "./pages/Login";
import { GroupDetail } from "./pages/GroupDetail";
import { NewGroup } from "./pages/NewGroup";
import { NewMeeting } from "./pages/NewMeeting";
import { MyGroups } from "./pages/MyGroups";
import { AdminPage } from "./pages/Admin";
import { setOnUnauthorized } from "./api";

export default function App() {
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
    <Layout>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/login" element={<Login />} />
        <Route path="/groups/new" element={<NewGroup />} />
        <Route path="/groups/:id" element={<GroupDetail />} />
        <Route path="/groups/:id/meetings/new" element={<NewMeeting />} />
        <Route path="/my-groups" element={<MyGroups />} />
        <Route path="/admin" element={<AdminPage />} />
        <Route
          path="*"
          element={
            <div className="max-w-6xl mx-auto px-4 py-12 text-center text-gray-600">
              Page not found.
            </div>
          }
        />
      </Routes>
    </Layout>
  );
}
