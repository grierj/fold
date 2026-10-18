import { useAuth } from "../auth";
import { ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";

export function Missing({ title, body }: { title: string; body: string }) {
  return (
    <div className="max-w-xl mx-auto px-4 py-12 sm:py-16">
      <EmptyState
        level={1}
        illustration={<Illustration name="not-found" />}
        title={title}
        body={body}
        action={
          <ButtonLink to="/" variant="primary" layout="w-full sm:w-auto">
            Go home
          </ButtonLink>
        }
      />
    </div>
  );
}

export function NotFound() {
  const { user } = useAuth();
  return (
    <div className="max-w-xl mx-auto px-4 py-12 sm:py-16">
      <EmptyState
        level={1}
        illustration={<Illustration name="not-found" />}
        title="Page not found"
        body="The link may be old, or the page may have moved."
        action={
          <>
            <ButtonLink to="/" variant="primary" layout="w-full sm:w-auto">
              Go home
            </ButtonLink>
            {user && (
              <ButtonLink to="/my-groups" variant="secondary" layout="w-full sm:w-auto">
                My groups
              </ButtonLink>
            )}
          </>
        }
      />
    </div>
  );
}
