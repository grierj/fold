import type { ContactHistoryPeopleResponse, ContactHistoryResponse } from "../types";
import { api, apiCSV, path, query, type Id } from "./core";

export interface ContactHistoryFilters {
  purpose: string;
  action: string;
  from: string;
  until: string;
}

function historyPath(personId: Id, filters: ContactHistoryFilters, csv: boolean, before?: string) {
  const { purpose, action, from, until } = filters;
  return `${path`/admin/users/${personId}/contact-history`}${csv ? ".csv" : ""}${query({ purpose, action, from, until, before: csv ? undefined : before })}`;
}

export const ContactHistory = {
  people: (params: { q?: string; id?: string; email?: string }) =>
    api<ContactHistoryPeopleResponse>(`/admin/contact-history/people${query({ q: params.q, id: params.id, email: params.email })}`),
  list: (personId: Id, filters: ContactHistoryFilters, before?: string) =>
    api<ContactHistoryResponse>(historyPath(personId, filters, false, before)),
  csv: (personId: Id, filters: ContactHistoryFilters) => apiCSV(historyPath(personId, filters, true)),
};
