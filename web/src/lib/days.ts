// Calendar-day math on "YYYY-MM-DD" strings. A day is stepped as a UTC date, so
// daylight saving never moves it and the answer is the same in every browser
// zone. Anything that needs a zone's wall clock goes through lib/when instead.

function parts(day: string): [number, number, number] {
  const [y, m, d] = day.split("-").map(Number);
  return [y, m || 1, d || 1];
}

export function addDays(day: string, n: number): string {
  const [y, m, d] = parts(day);
  return new Date(Date.UTC(y, m - 1, d + n)).toISOString().slice(0, 10);
}

export function addMonths(day: string, n: number): string {
  const [y, m] = parts(day);
  return new Date(Date.UTC(y, m - 1 + n, 1)).toISOString().slice(0, 10);
}

// 0 = Sunday, the weekday of the calendar day itself.
export function weekdayIndex(day: string): number {
  const [y, m, d] = parts(day);
  return new Date(Date.UTC(y, m - 1, d)).getUTCDay();
}
