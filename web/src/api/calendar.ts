import type { CalendarResponse, RoomCalendar } from "../types";
import { api, query } from "./core";

export const Calendar = {
  list: (q: { from: string; to: string; tz: string; mine?: boolean; group?: string; deleted?: boolean; remote?: boolean; childcare?: boolean; church?: boolean }) =>
    api<CalendarResponse>(`/calendar${query(q)}`),
  // The by-room view (M2.2a): one day or the Sunday-to-Saturday week holding
  // date, in the church's zone. 404 while rooms are off and for teens.
  rooms: (q: { view: "day" | "week"; date?: string }) => api<RoomCalendar>(`/calendar/rooms${query(q)}`),
};
