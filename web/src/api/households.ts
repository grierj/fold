import type { Household, HouseholdConversion, HouseholdPerson } from "../types";
import { api, path, type Id } from "./core";

export interface HouseholdPersonInput {
  firstName: string;
  lastName?: string;
  kind: HouseholdPerson["kind"];
  birthDate?: string;
  careNotes?: string;
  photoConsent?: boolean;
}

// GET never creates: it answers {household: null} until the household
// exists, and always for teen accounts (Warden, PR 106).
export const Households = {
  convert: (id: Id, email: string) =>
    api<{ conversion: HouseholdConversion }>(path`/me/household/people/${id}/convert`, { method: "POST", body: { email } }),
  resendConversion: (id: Id) =>
    api<{ conversion: HouseholdConversion }>(path`/me/household/people/${id}/convert/resend`, { method: "POST" }),
  cancelConversion: (id: Id) => api<Record<string, never>>(path`/me/household/people/${id}/convert`, { method: "DELETE" }),
  mine: () => api<{ household: Household | null }>("/me/household"),
  update: (data: { name?: string; address?: string }) => api<{ household: Household }>("/me/household", { method: "PUT", body: data }),
  addPerson: (data: HouseholdPersonInput) => api<{ person: HouseholdPerson }>("/me/household/people", { method: "POST", body: data }),
  updatePerson: (id: Id, data: HouseholdPersonInput) =>
    api<{ person: HouseholdPerson }>(path`/me/household/people/${id}`, { method: "PUT", body: data }),
  removePerson: (id: Id) => api<Record<string, never>>(path`/me/household/people/${id}`, { method: "DELETE" }),
};
