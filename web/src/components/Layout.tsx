import { useEffect, useState } from "react";
import { Link, NavLink } from "react-router-dom";
import { Site } from "../api";
import { useAuth } from "../auth";
import { Avatar } from "./Avatar";
import { CalendarIcon, GroupsIcon, HomeIcon, MeIcon, PlusIcon } from "./icons";
import { NotificationBell, NotificationTab } from "./Notifications";
import { FoldMark } from "./FoldMark";

// The app shell (UI makeover PR 5): a header with the church's name, a top
// nav from the sm breakpoint up, and a bottom tab bar on phones (Home,
// Calendar, My groups, Notifications, Me). Every control is at least 44px. The bars sit
// inside the safe areas (viewport-fit=cover in index.html), and the page
// keeps room under the tab bar so it never covers a focused field
// (scroll-padding in index.css).
const topLink = ({ isActive }: { isActive: boolean }) =>
  `inline-flex items-center min-h-11 px-2 rounded-md ${isActive ? "text-primary" : "text-text-muted hover:text-primary"}`;
const iconLink = ({ isActive }: { isActive: boolean }) =>
  `inline-flex items-center justify-center min-h-11 min-w-11 rounded-full ${isActive ? "text-primary" : "text-text-muted hover:text-primary"}`;
const tab = ({ isActive }: { isActive: boolean }) =>
  `flex flex-1 flex-col items-center justify-center gap-0.5 min-h-14 whitespace-nowrap text-xs font-medium ${isActive ? "text-primary" : "text-text-muted"}`;

function useSiteName() {
  const [name, setName] = useState<string | undefined>(undefined);
  useEffect(() => {
    Site.get().then(
      (s) => setName(s.name),
      () => setName("")
    );
  }, []);
  return name;
}

export function Layout({ children }: { children: React.ReactNode }) {
  const { user, loading, logout, isAdmin } = useAuth();
  const site = useSiteName();
  const signedIn = !loading && !!user;

  return (
    <div className="min-h-screen flex flex-col">
      <header className="bg-surface border-b border-border sticky top-0 z-10 pt-[env(safe-area-inset-top)] pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)]">
        <div className="max-w-6xl mx-auto px-4 h-16 flex items-center gap-4">
          <Link
            to="/"
            aria-label={site === undefined ? "Home" : undefined}
            className="flex min-h-11 min-w-0 flex-col justify-center leading-tight"
          >
            {site === undefined ? (
              <span aria-hidden="true" className="bg-sunken rounded-md h-6 w-32" />
            ) : site && site !== "Fold" ? (
              <>
                <span className="text-xl font-bold text-primary tracking-tight truncate">{site}</span>
                <span className="inline-flex items-center gap-1 text-xs text-text-subtle whitespace-nowrap">
                  <FoldMark className="w-3 h-3" />
                  powered by Fold
                </span>
              </>
            ) : (
              <span className="inline-flex items-center gap-1.5 text-xl font-bold text-primary">
                <FoldMark className="w-6 h-6" />
                Fold
              </span>
            )}
          </Link>
          <nav aria-label="Main" className="flex items-center gap-2 ml-auto text-sm font-medium">
            {loading ? null : user ? (
              <>
                <div className="hidden sm:flex items-center gap-2">
                  <NavLink to="/" end className={topLink}>Home</NavLink>
                  <NavLink to="/calendar" className={topLink}>Calendar</NavLink>
                  <NavLink to="/my-groups" className={topLink}>My groups</NavLink>
                  <NavLink to="/groups/new" className={topLink}>Start a group</NavLink>
                  {isAdmin && <NavLink to="/admin" className={topLink}>Admin</NavLink>}
                </div>
                {/* On phones, New group is a labeled header link, Admin is a row on
                    the Me page, and the rest are tabs (PR #65 review F2). */}
                <NavLink to="/groups/new" className={(a) => `sm:hidden shrink-0 whitespace-nowrap gap-1 px-2 text-sm font-medium ${iconLink(a)}`}>
                  <PlusIcon />
                  New group
                </NavLink>
                <div className="hidden sm:flex items-center gap-2">
                  <NotificationBell />
                  <Link to="/profile" className="flex items-center gap-2 min-h-11 px-1 text-text-muted hover:text-primary" title="Your profile">
                    <Avatar
                      url={user.avatarUrl || undefined}
                      name={user.displayName || user.name}
                      focalX={user.avatarFocalX}
                      focalY={user.avatarFocalY}
                      size="w-8 h-8"
                      textSize="text-sm"
                    />
                    <span className="hidden md:inline">{user.displayName || user.name}</span>
                  </Link>
                  <button onClick={logout} className="inline-flex items-center min-h-11 px-2 text-text-muted hover:text-primary">
                    Log out
                  </button>
                </div>
              </>
            ) : (
              <>
                <NavLink to="/calendar" className={topLink}>Calendar</NavLink>
                <NavLink to="/join" className={topLink}>Join</NavLink>
                <NavLink to="/login" className="btn-primary text-sm">Log in</NavLink>
              </>
            )}
          </nav>
        </div>
      </header>
      <main className={`flex-1 ${signedIn ? "pb-[calc(3.5rem+env(safe-area-inset-bottom))] sm:pb-0" : ""}`}>{children}</main>
      <footer className={`bg-surface border-t border-border mt-12 ${signedIn ? "mb-[calc(3.5rem+env(safe-area-inset-bottom))] sm:mb-0" : ""}`}>
        <div className="max-w-6xl mx-auto px-4 py-6 text-sm text-text-muted">
          {site && site !== "Fold" ? "Powered by Fold" : "Fold — gather your church community."}
        </div>
      </footer>
      {signedIn && (
        <nav
          aria-label="Tabs"
          className="sm:hidden fixed inset-x-0 bottom-0 z-20 flex bg-surface border-t border-border pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)]"
        >
          <NavLink to="/" end className={tab}>
            <HomeIcon />
            Home
          </NavLink>
          <NavLink to="/calendar" className={tab}>
            <CalendarIcon />
            Calendar
          </NavLink>
          <NavLink to="/my-groups" className={tab}>
            <GroupsIcon />
            My groups
          </NavLink>
          <NotificationTab className={tab} />
          <NavLink to="/profile" className={tab}>
            <MeIcon />
            Me
          </NavLink>
        </nav>
      )}
    </div>
  );
}
