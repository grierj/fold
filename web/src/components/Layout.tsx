import { Link, NavLink, useNavigate } from "react-router-dom";
import { useAuth } from "../auth";
import { Avatar } from "./Avatar";
import { NotificationBell } from "./Notifications";

export function Layout({ children }: { children: React.ReactNode }) {
  const { user, loading, logout, isAdmin } = useAuth();
  const navigate = useNavigate();

  async function handleLogout() {
    await logout();
    navigate("/");
  }

  return (
    <div className="min-h-screen flex flex-col">
      <header className="bg-white border-b border-gray-200 sticky top-0 z-10">
        <div className="max-w-6xl mx-auto px-4 h-16 flex items-center gap-6">
          <Link to="/" className="text-2xl font-extrabold text-brand tracking-tight">
            Fold
          </Link>
          <nav className="flex items-center gap-4 ml-auto text-sm font-medium">
            <NavLink
              to="/groups/new"
              className="hidden sm:inline text-gray-700 hover:text-brand"
            >
              Start a group
            </NavLink>
            {loading ? null : user ? (
              <>
                <NavLink
                  to="/my-groups"
                  className={({ isActive }) =>
                    isActive ? "text-brand" : "text-gray-700 hover:text-brand"
                  }
                >
                  My groups
                </NavLink>
                {isAdmin && (
                  <NavLink
                    to="/admin"
                    className={({ isActive }) =>
                      isActive ? "text-brand" : "text-gray-700 hover:text-brand"
                    }
                  >
                    Admin
                  </NavLink>
                )}
                <NotificationBell />
                <Link
                  to="/profile"
                  className="hidden sm:flex items-center gap-2 text-gray-700 hover:text-brand"
                  title="Your profile"
                >
                  <Avatar
                    url={user.avatarUrl || undefined}
                    name={user.displayName || user.name || user.username}
                    focalX={user.avatarFocalX}
                    focalY={user.avatarFocalY}
                    size="w-8 h-8"
                    textSize="text-sm"
                  />
                  <span className="text-gray-600">
                    {user.displayName || user.name || user.username}
                  </span>
                </Link>
                <button onClick={handleLogout} className="text-gray-700 hover:text-brand">
                  Log out
                </button>
              </>
            ) : (
              <NavLink
                to="/login"
                className="btn-primary !py-1.5 !px-4 text-sm"
              >
                Log in
              </NavLink>
            )}
          </nav>
        </div>
      </header>
      <main className="flex-1">{children}</main>
      <footer className="bg-white border-t border-gray-200 mt-12">
        <div className="max-w-6xl mx-auto px-4 py-6 text-sm text-gray-500">
          Fold — gather your church community.
        </div>
      </footer>
    </div>
  );
}
