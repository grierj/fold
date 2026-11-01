import { useLayoutEffect, useRef } from "react";
import { Link, NavLink, useLocation } from "react-router-dom";
import { useSiteName } from "../lib/useSiteName";
import { usePageTitle } from "../lib/usePageTitle";
import { useAuth } from "../auth";
import { useBadge } from "../lib/notifications";
import { Avatar } from "./Avatar";
import { CalendarIcon, GroupsIcon, HomeIcon, MeIcon, PlusIcon } from "./icons";
import { NotificationBell, NotificationTab } from "./Notifications";
import { FoldMark } from "./FoldMark";

// The app shell (UI makeover PR 5): a header with the church's name, a top
// nav from the sm breakpoint up, and a bottom tab bar on phones (Home,
// Calendar, My groups, Notifications, Me). Every shell control is at least 44px. The bars sit
// inside the safe areas (viewport-fit=cover in index.html), and the page
// keeps room under the tab bar so it never covers a focused field
// (scroll-padding in index.css).
const topLink = ({ isActive }: { isActive: boolean }) =>
  `fold-nav-link inline-flex items-center min-h-11 px-2 rounded-md whitespace-nowrap ${isActive ? "text-primary" : "text-text-muted hover:text-primary"}`;
// The phone's "+ New group" (DEC-62: a labeled pill, not a bare icon).
const pill = ({ isActive }: { isActive: boolean }) =>
  `inline-flex items-center gap-1 min-h-11 px-3 rounded-full border whitespace-nowrap font-semibold ${isActive ? "border-primary-border bg-primary-subtle text-primary-on-subtle" : "border-border-strong text-primary"}`;
const tab = ({ isActive }: { isActive: boolean }) =>
  `fold-tab flex flex-1 flex-col items-center justify-center gap-0.5 min-h-14 whitespace-nowrap text-xs font-medium ${isActive ? "text-primary" : "text-text-muted"}`;

export function Layout({ children }: { children: React.ReactNode }) {
  const { pathname, search } = useLocation();
  const { user, loading, logout, isAdmin, canCompose } = useAuth();
  const site = useSiteName();
  usePageTitle(site, pathname);
  const header = useRef<HTMLElement>(null);
  useLayoutEffect(() => {
    const element = header.current;
    if (!element) return;
    const update = () => document.documentElement.style.setProperty("--fold-header-height", `${element.getBoundingClientRect().height}px`);
    update();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
    observer?.observe(element);
    return () => { observer?.disconnect(); document.documentElement.style.removeProperty("--fold-header-height"); };
  }, []);
  const plainLogin = ["/login", "/join", "/forgot-password"].some((p) => pathname === p || pathname.startsWith(`${p}/`));
  const signedIn = !loading && !!user;
  // One poller for the bell and the phone tab (they show the same count).
  const badge = useBadge(pathname + search, signedIn);

  return (
    <div className="min-h-screen flex flex-col">
      <a href="#main" onClick={(event) => {
        event.preventDefault();
        const main = document.getElementById("main");
        main?.focus({ preventScroll: true });
        main?.scrollIntoView({ block: "start" });
      }} className="fixed top-2 left-2 z-50 -translate-y-[200%] focus:translate-y-0 rounded-md bg-surface p-3 min-h-11 text-primary focus:outline-2 focus:outline-focus">Skip to main content</a>
      <header ref={header} className="bg-surface border-b border-border sticky top-0 z-10 pt-[env(safe-area-inset-top)] pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)]">
        <div className="max-w-6xl mx-auto px-4 sm:px-6 min-h-16 py-2 flex items-center gap-3 sm:gap-4">
          <Link
            to="/"
            aria-label={site === undefined ? "Home" : undefined}
            className="flex flex-1 sm:flex-initial min-h-11 min-w-0 flex-col justify-center leading-tight"
          >
            {site === undefined ? (
              <span aria-hidden="true" className="bg-sunken rounded-md h-6 w-32" />
            ) : site && site !== "Fold" ? (
              <>
                <span className="text-lg sm:text-xl font-bold text-primary tracking-tight leading-tight line-clamp-2 sm:line-clamp-none">{site}</span>
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
          <nav aria-label="Main" className="flex shrink-0 items-center gap-1 ml-auto text-sm font-medium">
            {loading ? null : user ? (
              <>
                <div className="hidden sm:flex items-center gap-1">
                  <NavLink to="/" end className={topLink}>Home</NavLink>
                  <NavLink to="/calendar" className={topLink}>Calendar</NavLink>
                  <NavLink to="/my-groups" className={topLink}>My groups</NavLink>
                  {/* Messages joins the nav from lg up and is on Me below that: at
                      768px one more link squeezed the church name (to 13px for an
                      admin, 20px for a leader; layout.spec.ts, name-search.spec.ts). */}
                  {canCompose && <span className="hidden lg:contents"><NavLink to="/messages" className={topLink}>Messages</NavLink></span>}
                  <NavLink to="/groups/new" className={topLink}>New group</NavLink>
                  {isAdmin && <NavLink to="/admin" className={topLink}>Admin</NavLink>}
                </div>
                {/* On phones a labeled pill that leaves the church's name its room
                    (the name clamps at two lines); Messages and Admin are on Me. */}
                <NavLink to="/groups/new" className={(a) => `sm:hidden shrink-0 ${pill(a)}`}>
                  <PlusIcon className="h-4 w-4" />
                  New group
                </NavLink>
                <div className="hidden sm:flex items-center gap-1">
                  <NotificationBell badge={badge} />
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
                <div className="hidden sm:block"><NavLink to="/calendar" className={topLink}>Calendar</NavLink></div>
                <NavLink to="/join" className={topLink}>Sign up</NavLink>
                {/* On an invitation the form is the main action, and a waiting teen
                    can't sign in yet, so Log in steps back (PR 111 N3). On the
                    sign-in, join and password pages the form has its own button,
                    so the header's is a plain link (DEC-99). */}
                <NavLink to="/login" className={plainLogin ? topLink : `${pathname.startsWith("/invite") ? "btn-secondary" : "btn-primary"} text-sm whitespace-nowrap`}>Log in</NavLink>
              </>
            )}
          </nav>
        </div>
      </header>
      <main id="main" tabIndex={-1} className={`flex-1 scroll-mt-[calc(var(--fold-header-height,4rem)+1rem)] ${signedIn ? "pb-[calc(3.5rem+env(safe-area-inset-bottom))] sm:pb-0" : ""}`}>{children}</main>
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
          <NotificationTab badge={badge} className={tab} />
          <NavLink to="/profile" className={tab}>
            <MeIcon />
            Me
          </NavLink>
        </nav>
      )}
    </div>
  );
}
