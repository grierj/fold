// A page that couldn't load says so in words from lib/errors.ts and offers
// to try again. The box leaves room for the "can't load" illustration (I4)
// that UI makeover PR 8 adds.
export function ErrorNotice({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div role="alert" className="rounded-lg border border-danger-border bg-danger-subtle p-card text-danger-on-subtle sm:p-card-lg">
      <p>{message}</p>
      {onRetry && (
        <button type="button" className="btn-secondary mt-3" onClick={onRetry}>
          Try again
        </button>
      )}
    </div>
  );
}
