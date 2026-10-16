import { Link, NavLink } from "react-router-dom";
import { useAuth } from "../auth";
import { Avatar } from "./Avatar";
import { NotificationBell } from "./Notifications";

export function Layout({ children }: { children: React.ReactNode }) {
  const { user, loading, logout, isAdmin } = useAuth();


  return (
    <div className="min-h-screen flex flex-col">
      <header className="bg-surface border-b border-border sticky top-0 z-10">
        <div className="max-w-6xl mx-auto px-4 h-16 flex items-center gap-6">
          <Link to="/" className="text-2xl font-bold text-primary tracking-tight">
            Fold
          </Link>
          <nav className="flex items-center gap-4 ml-auto text-sm font-medium">
            <NavLink
              to="/groups/new"
              className="hidden sm:inline text-text-muted hover:text-primary"
            >
              Start a group
            </NavLink>
            {loading ? null : user ? (
              <>
                <NavLink
                  to="/my-groups"
                  className={({ isActive }) =>
                    isActive ? "text-primary" : "text-text-muted hover:text-primary"
                  }
                >
                  My groups
                </NavLink>
                {isAdmin && (
                  <NavLink
                    to="/admin"
                    className={({ isActive }) =>
                      isActive ? "text-primary" : "text-text-muted hover:text-primary"
                    }
                  >
                    Admin
                  </NavLink>
                )}
                <NotificationBell />
                <Link
                  to="/profile"
                  className="hidden sm:flex items-center gap-2 text-text-muted hover:text-primary"
                  title="Your profile"
                >
                  <Avatar
                    url={user.avatarUrl || undefined}
                    name={user.displayName || user.name}
                    focalX={user.avatarFocalX}
                    focalY={user.avatarFocalY}
                    size="w-8 h-8"
                    textSize="text-sm"
                  />
                  <span className="text-text-muted">
                    {user.displayName || user.name}
                  </span>
                </Link>
                <button onClick={logout} className="text-text-muted hover:text-primary">
                  Log out
                </button>
              </>
            ) : (
              <>
                <NavLink to="/join" className="text-text-muted hover:text-primary">
                  Join
                </NavLink>
                <NavLink
                  to="/login"
                  className="btn-primary py-1.5! px-4! text-sm"
                >
                  Log in
                </NavLink>
              </>
            )}
          </nav>
        </div>
      </header>
      <main className="flex-1">{children}</main>
      <footer className="bg-surface border-t border-border mt-12">
        <div className="max-w-6xl mx-auto px-4 py-6 text-sm text-text-muted">
          Fold — gather your church community.
        </div>
      </footer>
    </div>
  );
}
