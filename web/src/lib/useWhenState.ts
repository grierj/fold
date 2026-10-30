import { useState } from "react";
import { weekdayCode } from "./eventForm";

export interface WhenInit {
  date: string;
  time: string;
  duration: number;
  recurring: boolean;
  days: string[];
}

// The state behind the When group on every event form (DEC-95): date, start
// time, duration and a weekly repeat. Turning on Every week, or picking a
// date while it is on with no day chosen, selects the date's weekday.
export function useWhenState(init: WhenInit) {
  const [date, setDateRaw] = useState(init.date);
  const [time, setTime] = useState(init.time);
  const [duration, setDuration] = useState(init.duration);
  const [recurring, setRecurringRaw] = useState(init.recurring);
  const [days, setDays] = useState<string[]>(init.days);

  function setDate(v: string) {
    setDateRaw(v);
    if (recurring && days.length === 0 && v) setDays([weekdayCode(v)]);
  }
  function setRecurring(on: boolean) {
    setRecurringRaw(on);
    if (on && days.length === 0 && date) setDays([weekdayCode(date)]);
  }
  function toggleDay(v: string) {
    setDays((prev) => (prev.includes(v) ? prev.filter((d) => d !== v) : [...prev, v]));
  }
  return { date, setDate, time, setTime, duration, setDuration, recurring, setRecurring, days, toggleDay };
}

export type WhenState = ReturnType<typeof useWhenState>;
