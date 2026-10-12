import { Link } from "react-router-dom";
import type { GroupSummary } from "../types";
import { focalPosition } from "../lib/image";
import { formatNextEvent } from "../lib/wall";

const PLACEHOLDER =
  "data:image/svg+xml," +
  encodeURIComponent(
    `<svg xmlns="http://www.w3.org/2000/svg" width="640" height="360"><defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#f64060"/><stop offset="1" stop-color="#7c3aed"/></linearGradient></defs><rect width="640" height="360" fill="url(#g)"/></svg>`
  );

export function coverImage(url: string | undefined | null): string {
  return url && url.trim() ? url : PLACEHOLDER;
}

// Meetup-style group card: cover image, name, member count, next event, location.
// Tags sit outside the link so they can toggle the directory filter.
// Tags match case-insensitively (as the directory filter does).
export function GroupCard({
  group,
  onTagClick,
  selectedTags = [],
}: {
  group: GroupSummary;
  onTagClick?: (tag: string) => void;
  selectedTags?: string[];
}) {
  const selectedLower = selectedTags.map((t) => t.toLowerCase());
  const isSelected = (t: string) => selectedLower.includes(t.toLowerCase());
  return (
    <div className="bg-white rounded-xl shadow-sm hover:shadow-md transition-shadow overflow-hidden border border-gray-100 flex flex-col">
      <Link to={`/groups/${group.id}`} className="block">
        <div className="h-40 w-full overflow-hidden bg-gray-200">
          <img
            src={coverImage(group.imageUrl)}
            alt=""
            className="w-full h-full object-cover"
            style={{ objectPosition: focalPosition(group.coverFocalX, group.coverFocalY) }}
            loading="lazy"
          />
        </div>
        <div className="px-4 pt-4">
          <h3 className="font-bold text-lg leading-snug">{group.name}</h3>
          {(group.status === "pending" ||
            group.status === "archived" ||
            group.visibility !== "public") && (
            <div className="flex flex-wrap gap-1 mt-1">
              {group.status === "pending" && (
                <span className="text-xs font-bold uppercase tracking-wide text-yellow-800">
                  Pending approval
                </span>
              )}
              {group.status === "archived" && (
                <span className="text-xs font-bold uppercase tracking-wide text-gray-500">Archived</span>
              )}
              {group.visibility === "private" && (
                <span className="text-xs font-bold uppercase tracking-wide text-gray-500">Private</span>
              )}
              {group.visibility === "invite_only" && (
                <span className="inline-block text-xs font-bold uppercase tracking-wide bg-gray-900 text-white rounded-full px-2 py-0.5">
                  Invite only
                </span>
              )}
            </div>
          )}
        </div>
      </Link>
      {group.tags.length > 0 && (
        <div className="px-4 pt-2 flex flex-wrap gap-1">
          {group.tags.slice(0, 3).map((t) =>
            onTagClick ? (
              <button
                key={t}
                type="button"
                onClick={() => onTagClick(t)}
                aria-pressed={isSelected(t)}
                className={
                  isSelected(t)
                    ? "inline-flex items-center gap-0.5 text-xs font-semibold bg-brand text-white rounded-full px-2 py-0.5 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand focus-visible:ring-offset-1"
                    : "text-xs font-medium bg-gray-100 text-gray-600 hover:bg-gray-200 rounded-full px-2 py-0.5 focus:outline-none focus-visible:ring-2 focus-visible:ring-brand focus-visible:ring-offset-1"
                }
              >
                {isSelected(t) && <span aria-hidden="true">✓</span>}
                {t}
              </button>
            ) : (
              <span
                key={t}
                className="text-xs font-medium bg-gray-100 text-gray-600 rounded-full px-2 py-0.5"
              >
                {t}
              </span>
            )
          )}
        </div>
      )}
      <Link to={`/groups/${group.id}`} className="block px-4 pb-4 pt-2 mt-auto">
        <div className="text-sm text-gray-600 space-y-0.5">
          <div>
            <span className="font-semibold text-gray-800">{group.memberCount}</span>{" "}
            {group.memberCount === 1 ? "member" : "members"}
            {group.leaderName ? ` · Organized by ${group.leaderName}` : ""}
          </div>
          <div className="text-brand-dark font-medium">
            {formatNextEvent(group.nextEventLocal, group.nextEventTzid, group.nextEventAt)}
          </div>
          {group.location && <div className="truncate">{group.location}</div>}
        </div>
      </Link>
    </div>
  );
}
