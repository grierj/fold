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
    <div role="alert" className="flex flex-col items-center rounded-lg border border-border bg-surface p-6 sm:p-8 text-center">
      <div className="mb-4">
        <Illustration name="cant-load" />
      </div>
      <p className="text-base text-text">{message}</p>
      <Button variant="secondary" layout="mt-4 w-full sm:w-auto" onClick={onRetry}>
        Try again
      </Button>
    </div>
  );
}
