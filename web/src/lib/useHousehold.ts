import { useEffect, useState } from "react";
import { Households } from "../api";
import type { HouseholdPerson, User } from "../types";
import { errorText } from "./errors";

// One read per page/viewer; date cards share the same selectable people.
export function useHousehold(user: User | null) {
  const [people, setPeople] = useState<HouseholdPerson[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let current = true;
    setPeople([]); setError(null);
    if (!user || user.accountType === "teen") return;
    Households.mine().then(({ household }) => { if (current) setPeople(household?.people ?? []); }).catch((e) => { if (current) setError(errorText(e, "We couldn't load your household.")); });
    return () => { current = false; };
  }, [user?.id, user?.accountType, attempt]);
  return { people, error, retry: () => setAttempt((a) => a + 1) };
}
