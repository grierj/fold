import { Button } from "./ui/Button";
import { Illustration } from "./ui/Illustration";

// A page that couldn't load says so in words from lib/errors.ts and offers
// to try again. With onRetry it is a neutral card with the "can't load"
// illustration; without one it stays the inline danger notice.
export function ErrorNotice({ message, onRetry }: { message: string; onRetry?: () => void }) {
  if (!onRetry) {
    return (
      <div role="alert" className="rounded-lg border border-danger-border bg-danger-subtle p-card text-danger-on-subtle sm:p-card-lg">
        <p>{message}</p>
      </div>
    );
  }
  return (
    <div role="alert" className="flex flex-col items-start gap-4 rounded-lg bg-surface p-6 sm:flex-row sm:gap-6">
      <div className="w-16 shrink-0">
        <Illustration name="cant-load" />
      </div>
      <div className="min-w-0 w-full">
        <p className="text-base text-text max-w-prose">{message}</p>
        <Button variant="secondary" layout="mt-4 w-full sm:w-auto" onClick={onRetry}>
          Try again
        </Button>
      </div>
    </div>
  );
}
