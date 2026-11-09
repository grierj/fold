import { Households } from "../api/households";
import type { User } from "../types";
import { errorText } from "./errors";
import { useLoad } from "./useLoad";

// One read per page/viewer; date cards share the same selectable people.
export function useHousehold(user: User | null) {
  const load = useLoad(
    async () => (await Households.mine()).household?.people ?? [],
    [user?.id, user?.accountType],
    { enabled: !!user && user.accountType !== "teen" },
  );
  return { people: load.data ?? [], error: load.error ? errorText(load.error, "We couldn't load your household.") : null, retry: load.retry };
}
