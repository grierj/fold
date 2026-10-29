import { api, authenticatedCSV } from "./api";
import type { ContactHistoryPeopleResponse, ContactHistoryResponse } from "./types";

export interface ContactHistoryFilters {
  purpose: string;
  action: string;
  from: string;
  until: string;
}

// Retained person IDs and audit cursors are exact decimal strings. Never
// coerce them to JS numbers; unrelated user-list IDs are still numeric.
export function retainedPersonId(value: string): string | null {
  if (!/^[0-9]+$/.test(value)) return null;
  const id = value.replace(/^0+/, "");
  if (!id || id.length > 19 || (id.length === 19 && id > "9223372036854775807")) return null;
  return id;
}

function historyPath(personId: string, filters: ContactHistoryFilters, csv: boolean, before?: string) {
  const query = new URLSearchParams();
  for (const key of ["purpose", "action", "from", "until"] as const) {
    if (filters[key]) query.set(key, filters[key]);
  }
  if (!csv && before) query.set("before", before);
  const suffix = query.size ? `?${query}` : "";
  return `/admin/users/${encodeURIComponent(personId)}/contact-history${csv ? ".csv" : ""}${suffix}`;
}

export const ContactHistory = {
  people: (params: { q?: string; id?: string; email?: string }) => {
    const query = new URLSearchParams();
    for (const key of ["q", "id", "email"] as const) {
      if (params[key]) query.set(key, params[key]);
    }
    const suffix = query.size ? `?${query}` : "";
    return api<ContactHistoryPeopleResponse>(`/admin/contact-history/people${suffix}`);
  },
  list: (personId: string, filters: ContactHistoryFilters, before?: string) =>
    api<ContactHistoryResponse>(historyPath(personId, filters, false, before)),
  csv: (personId: string, filters: ContactHistoryFilters) =>
    authenticatedCSV(historyPath(personId, filters, true)),
};
