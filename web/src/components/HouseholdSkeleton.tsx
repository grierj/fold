import { Skeleton } from "./ui/Skeleton";

// Shared by the lazy chunk fallback and the card's data-loading state.
// showHeading=false when a page already has the h1 "Household" above it.
export function HouseholdSkeleton({ showHeading = true }: { showHeading?: boolean }) {
  return <section aria-label="My household loading" className={`${showHeading ? "mt-6 " : ""}min-h-80 space-y-4`}>
    {showHeading && <h2 className="text-xl font-bold mb-1">My household</h2>}
    <div className="rounded-lg border border-border p-4 min-h-60">
      <Skeleton kind="page" label="Loading your household…" />
    </div>
  </section>;
}
