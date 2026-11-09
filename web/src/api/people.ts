import type { PeopleSearchResponse } from "../types";
import { api, query } from "./core";

export const People = {
  search: (q: string) => api<PeopleSearchResponse>(`/people/search${query({ q })}`),
};
