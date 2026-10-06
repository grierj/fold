import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Notifications as NotificationsApi } from "../api";
import type { Notification } from "../types";

const DISMISSED_KEY = "fold-dismissed-notifications";

function dismissedIds(): Set<string> {
  try {
    return new Set(JSON.parse(localStorage.getItem(DISMISSED_KEY) ?? "[]"));
  } catch {
    return new Set();
  }
}

function notifId(n: Notification): string {
  return `${n.type}:${n.link}:${n.title}`;
}

// NotificationBell: derived notifications (approvals for admins, upcoming
// events). Dismissals are tracked in localStorage; the list itself is
// recomputed server-side on each load.
export function NotificationBell() {
  const [items, setItems] = useState<Notification[]>([]);
  const [open, setOpen] = useState(false);
  const [dismissed, setDismissed] = useState<Set<string>>(dismissedIds);
  const boxRef = useRef<HTMLDivElement>(null);

  const load = useCallback(async () => {
    try {
      const { notifications } = await NotificationsApi.list();
      setItems(notifications);
    } catch {
      // Notifications are best-effort; never break the header.
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 5 * 60 * 1000);
    return () => clearInterval(t);
  }, [load]);

  useEffect(() => {
    function onDocClick(e: MouseEvent) {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("click", onDocClick);
    return () => document.removeEventListener("click", onDocClick);
  }, []);

  const visible = items.filter((n) => !dismissed.has(notifId(n)));

  function dismiss(n: Notification) {
    const id = notifId(n);
    setDismissed((prev) => {
      const next = new Set(prev);
      next.add(id);
      localStorage.setItem(DISMISSED_KEY, JSON.stringify([...next]));
      return next;
    });
  }

  return (
    <div ref={boxRef} className="relative">
      <button
        onClick={() => setOpen((o) => !o)}
        className="relative text-gray-700 hover:text-brand p-1"
        title="Notifications"
        aria-label="Notifications"
      >
        <svg className="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9"
          />
        </svg>
        {visible.length > 0 && (
          <span className="absolute -top-1 -right-1 min-w-[1.25rem] h-5 px-1 rounded-full bg-brand text-white text-xs font-bold flex items-center justify-center">
            {visible.length}
          </span>
        )}
      </button>
      {open && (
        <div className="absolute right-0 mt-2 w-80 max-w-[90vw] bg-white rounded-xl border border-gray-200 shadow-lg z-20 overflow-hidden">
          <div className="px-4 py-3 border-b border-gray-100 font-bold text-sm">
            Notifications
          </div>
          {visible.length === 0 ? (
            <div className="px-4 py-6 text-sm text-gray-500 text-center">
              All caught up.
            </div>
          ) : (
            <ul className="divide-y divide-gray-100 max-h-96 overflow-y-auto">
              {visible.map((n) => (
                <li key={notifId(n)}>
                  <Link
                    to={n.link}
                    onClick={() => {
                      dismiss(n);
                      setOpen(false);
                    }}
                    className="block px-4 py-3 hover:bg-gray-50"
                  >
                    <div className="flex items-start gap-2">
                      <span
                        className={`mt-1 w-2 h-2 rounded-full shrink-0 ${
                          n.type === "approval" ? "bg-yellow-500" : "bg-brand"
                        }`}
                      />
                      <div className="min-w-0">
                        <div className="font-semibold text-sm">{n.title}</div>
                        <div className="text-xs text-gray-500">{n.body}</div>
                      </div>
                    </div>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
