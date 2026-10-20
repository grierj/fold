import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Calendar, Groups } from "../api";
import { useAuth } from "../auth";
import type { CalendarOccurrence, GroupSummary } from "../types";
import { addDays, addMonths, groupByDay, monthGrid, rangeFor, weekRange, type View } from "../lib/calendar";
import { browserTimeZone, dayOf, formatDayHeading, formatPeriod } from "../lib/when";
import { errorText } from "../lib/errors";
import { Button, ButtonLink } from "../components/ui/Button";
import { Chip } from "../components/ui/Chip";
import { Field, Select } from "../components/ui/Field";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { Skeleton } from "../components/ui/Skeleton";
import { ViewSwitch } from "../components/ui/ViewSwitch";
import { ErrorNotice } from "../components/ErrorNotice";
import { ChevronLeftIcon, ChevronRightIcon } from "../components/icons";
import { DaySection } from "../components/calendar/EventRow";
import { WeekView } from "../components/calendar/WeekView";
import { MonthView } from "../components/calendar/MonthView";

// The calendar (M1.6, DEC-67): every date the viewer may see, as Agenda
// (default), Week or Month. The URL holds the state, so Back and shared
// links work: ?view=&date=&mine=1&group=.
const AGENDA_STEP = 30;
const AGENDA_MAX = 366;

export function CalendarPage() {
  const { user } = useAuth();
  const [params, setParams] = useSearchParams();
  const zone = browserTimeZone();
  const today = dayOf(new Date().toISOString(), zone);
  const view: View = params.get("view") === "week" || params.get("view") === "month" ? (params.get("view") as View) : "agenda";
  const date = /^\d{4}-\d{2}-\d{2}$/.test(params.get("date") ?? "") ? params.get("date")! : today;
  const mine = !!user && params.get("mine") === "1";
  const group = params.get("group") ?? "";

  const [agendaDays, setAgendaDays] = useState(AGENDA_STEP);
  const [occs, setOccs] = useState<CalendarOccurrence[]>([]);
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [retry, setRetry] = useState(0);
  const [groups, setGroups] = useState<GroupSummary[]>([]);
  const [selected, setSelected] = useState<string | null>(null);

  const range = rangeFor(view, view === "agenda" ? today : date, agendaDays);

  useEffect(() => {
    Groups.list("", []).then(
      ({ groups }) => setGroups([...groups].sort((a, b) => a.name.localeCompare(b.name))),
      () => setGroups([])
    );
  }, []);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError(null);
    Calendar.list({ from: range.from, to: range.to, tz: zone, mine, group })
      .then(({ occurrences, truncated }) => {
        if (!alive) return;
        setOccs(occurrences);
        setTruncated(truncated);
      })
      .catch((e) => alive && setError(errorText(e, "We couldn't load the calendar.")))
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [range.from, range.to, zone, mine, group, retry]);

  function link(next: Record<string, string | null>): string {
    const p = new URLSearchParams(params);
    for (const [k, v] of Object.entries(next)) {
      if (v === null) p.delete(k);
      else p.set(k, v);
    }
    return `/calendar?${p}`;
  }
  function go(next: Record<string, string | null>) {
    setParams(new URLSearchParams(link(next).split("?")[1]), { replace: false });
  }

  const byDay = groupByDay(occs, zone);
  const weekDays = weekRange(date);
  const weeks = monthGrid(date);
  const month = date.slice(0, 7);
  const title = view === "month" ? formatPeriod("month", `${month}-01`, `${month}-01`) : formatPeriod("week", weekDays[0], weekDays[6]);
  const step = (n: number) => go({ date: view === "month" ? addMonths(date, n) : addDays(date, 7 * n) });
  const unit = view === "month" ? "month" : "week";
  const daySelected = selected && selected.slice(0, 7) === month ? selected : today.slice(0, 7) === month ? today : `${month}-01`;
  const groupName = groups.find((g) => String(g.id) === group)?.name ?? "this group";

  return (
    <div className="max-w-6xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-bold text-text">Calendar</h1>

      <div className="mt-6 flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-end">
        <ViewSwitch
          label="Calendar view"
          items={(["agenda", "week", "month"] as View[]).map((v) => ({
            label: v[0].toUpperCase() + v.slice(1),
            to: link({ view: v === "agenda" ? null : v }),
            current: v === view,
          }))}
        />
        {view !== "agenda" && (
          <div className="flex items-center gap-2">
            <Button variant="secondary" aria-label={`Previous ${unit}`} onClick={() => step(-1)}>
              <ChevronLeftIcon />
            </Button>
            <h2 aria-live="polite" className="min-w-0 flex-1 text-center font-semibold text-text sm:min-w-44">
              {title}
            </h2>
            <Button variant="secondary" aria-label={`Next ${unit}`} onClick={() => step(1)}>
              <ChevronRightIcon />
            </Button>
            <Button variant="ghost" onClick={() => go({ date: null })}>
              Today
            </Button>
          </div>
        )}
        <div className="flex items-end gap-3 sm:ml-auto">
          {user && (
            <Chip selected={mine} onClick={() => go({ mine: mine ? null : "1" })}>
              Just mine
            </Chip>
          )}
          <div className="min-w-0 flex-1 sm:w-56 sm:flex-none">
            <Field label="Group">
              <Select
                value={group}
                onChange={(e) => go({ group: e.target.value || null })}
                options={[{ value: "", label: "All groups" }, ...groups.map((g) => ({ value: String(g.id), label: g.name }))]}
              />
            </Field>
          </div>
        </div>
      </div>

      {!user && (
        <p className="mt-4 text-sm text-text-muted">
          Showing public events.{" "}
          <Link to="/login" className="font-semibold text-primary hover:underline">
            Log in
          </Link>{" "}
          to see your groups' events too.
        </p>
      )}

      {truncated && !error && (
        <p className="mt-4 text-sm text-text-muted">Showing the first 500 events. Narrow by group to see more.</p>
      )}

      <div className="mt-6">
        {error ? (
          <ErrorNotice message={error} onRetry={() => setRetry((n) => n + 1)} />
        ) : view === "agenda" ? (
          loading && occs.length === 0 ? (
            <Skeleton kind="lines" count={6} label="Loading the calendar…" />
          ) : occs.length === 0 ? (
            <AgendaEmpty mine={mine} group={group} groupName={groupName} signedIn={!!user} go={go} />
          ) : (
            <div className="max-w-3xl space-y-6">
              {[...byDay.entries()].map(([d, list]) => (
                <DaySection key={d} heading={formatDayHeading(d, zone)} occs={list} sticky />
              ))}
              {agendaDays < AGENDA_MAX ? (
                <Button variant="secondary" disabled={loading} onClick={() => setAgendaDays((n) => Math.min(n + AGENDA_STEP, AGENDA_MAX))}>
                  Show more dates
                </Button>
              ) : (
                <p className="text-sm text-text-muted">That's everything for the next year.</p>
              )}
            </div>
          )
        ) : (
          <>
            {!loading && occs.length === 0 && (
              <p className="mb-4 text-sm text-text-muted">{view === "week" ? "No events this week." : "No events this month."}</p>
            )}
            {view === "week" ? (
              <WeekView days={weekDays} byDay={byDay} today={today} />
            ) : (
              <MonthView
                weeks={weeks}
                month={month}
                byDay={byDay}
                today={today}
                selected={daySelected}
                onSelect={setSelected}
                onNavigate={(d) => {
                  setSelected(d);
                  go({ date: d });
                }}
                caption={title}
              />
            )}
            {loading && occs.length === 0 && <Skeleton kind="lines" label="Loading the calendar…" />}
          </>
        )}
      </div>
    </div>
  );
}

function AgendaEmpty({
  mine,
  group,
  groupName,
  signedIn,
  go,
}: {
  mine: boolean;
  group: string;
  groupName: string;
  signedIn: boolean;
  go: (next: Record<string, string | null>) => void;
}) {
  const art = <Illustration name="no-events" />;
  if (mine) {
    return (
      <EmptyState
        illustration={art}
        title="Nothing on your calendar"
        body="Events from your groups and ones you RSVP to show up here."
        action={<Button variant="secondary" onClick={() => go({ mine: null })}>Show everything</Button>}
      />
    );
  }
  if (group) {
    return (
      <EmptyState
        illustration={art}
        title={`No upcoming events for ${groupName}`}
        body="Pick another group, or show all groups."
        action={<Button variant="secondary" onClick={() => go({ group: null })}>Show all groups</Button>}
      />
    );
  }
  return (
    <EmptyState
      illustration={art}
      title="Nothing on the calendar yet"
      body="When groups schedule events, they'll show up here by date."
      action={signedIn ? <ButtonLink to="/" variant="secondary">Browse groups</ButtonLink> : undefined}
    />
  );
}
