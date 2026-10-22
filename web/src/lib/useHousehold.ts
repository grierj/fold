import { useEffect, useState } from "react";
import { Households } from "../api";
import type { HouseholdPerson, User } from "../types";

// One read per page/viewer; date cards share the same selectable people.
export function useHousehold(user: User | null) {
  const [people, setPeople] = useState<HouseholdPerson[]>([]);
  useEffect(() => {
    let current = true;
    setPeople([]);
    if (!user || user.accountType === "teen") return;
    Households.mine().then(({ household }) => { if (current) setPeople(household?.people ?? []); }).catch(() => { if (current) setPeople([]); });
    return () => { current = false; };
  }, [user?.id, user?.accountType]);
  return people;
}
