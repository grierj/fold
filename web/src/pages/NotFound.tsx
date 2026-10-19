import { useAuth } from "../auth";
import { ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";

// A page that isn't there (DEC-66): the title is the page's h1, and "Go
// home" is the way out. Group and event pages use Missing for a real 404
// only; any other load failure is an ErrorNotice with Try again.
export function Missing({ title, body, myGroups }: { title: string; body: string; myGroups?: boolean }) {
  return (
    <div className="max-w-xl mx-auto px-4 py-12 sm:py-16">
      <EmptyState
        level={1}
        illustration={<Illustration name="not-found" />}
        title={title}
        body={body}
        action={
          <>
            <ButtonLink to="/" variant="primary" layout="w-full sm:w-auto">
              Go home
            </ButtonLink>
            {myGroups && (
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

export function NotFound() {
  const { user } = useAuth();
  return <Missing title="Page not found" body="The link may be old, or the page may have moved." myGroups={!!user} />;
}
