import type { SiteInfo } from "../types";
import { api } from "./core";

export const Site = {
  get: () => api<SiteInfo>("/site"),
};
