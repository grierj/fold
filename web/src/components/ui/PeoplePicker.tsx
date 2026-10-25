import { useEffect, useRef, useState } from "react";
import { People } from "../../api";
import type { PeopleSearchResponse, PersonSearchResult } from "../../types";
import { errorText } from "../../lib/errors";
import { Avatar } from "../Avatar";
import { Badge } from "./Badge";
import { useField, wire } from "./Field";

const COPY = {
  short_query: "Type at least 2 letters",
  throttled: "Too many searches. Try again in a minute.",
  no_results: "No one found. An admin can add people who aren't listed.",
};

// Name-only search, not a directory. A selection is opaque and grants no
// permission to add; each destination still authorizes the add or ask.
export function PeoplePicker({ value, onChange, disabled = false }: {
  value: PersonSearchResult | null;
  onChange: (person: PersonSearchResult | null) => void;
  disabled?: boolean;
}) {
  const w = useField("PeoplePicker");
  const [query, setQuery] = useState("");
  const [response, setResponse] = useState<PeopleSearchResponse>({ state: "short_query", people: [] });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const generation = useRef(0);
  const listId = `${w.id}-list`;
  const statusId = `${w.id}-status`;
  const people = response.state === "results" ? response.people : [];
  const expanded = open && !disabled && !value && people.length > 0;
  const optionId = (i: number) => `${w.id}-person-${i}`;

  useEffect(() => {
    const current = ++generation.current;
    setError(null);
    setActive(-1);
    setResponse({ state: "short_query", people: [] });
    const trimmed = query.trim();
    if (disabled || value || Array.from(trimmed).length < 2) {
      setLoading(false);
      return () => { generation.current++; };
    }
    setLoading(true);
    const timer = window.setTimeout(() => {
      People.search(trimmed).then((result) => {
        if (generation.current !== current) return;
        setResponse(result);
        setLoading(false);
      }).catch((e: unknown) => {
        if (generation.current !== current) return;
        setError(errorText(e, "We couldn't search for people. Try again."));
        setLoading(false);
      });
    }, 300);
    return () => { window.clearTimeout(timer); generation.current++; };
  }, [query, value, disabled]);

  useEffect(() => {
    if (expanded && active >= 0) document.getElementById(optionId(active))?.scrollIntoView?.({ block: "nearest" });
  }, [expanded, active, w.id]);

  function pick(person: PersonSearchResult) {
    generation.current++;
    setQuery("");
    setOpen(false);
    setResponse({ state: "short_query", people: [] });
    onChange(person);
  }

  const duplicate = people.some((person, index) => people.some((other, j) => j !== index &&
    person.displayName.toLocaleLowerCase() === other.displayName.toLocaleLowerCase() &&
    person.teen === other.teen && person.avatarUrl === other.avatarUrl &&
    person.avatarFocalX === other.avatarFocalX && person.avatarFocalY === other.avatarFocalY &&
    JSON.stringify(person.sharedGroups) === JSON.stringify(other.sharedGroups) &&
    JSON.stringify(person.otherGroups) === JSON.stringify(other.otherGroups)));
  const status = value ? value.teen ? "Adding a teen needs their approval and an admin's approval. Choose Ask to add to continue." : `${value.displayName} selected` :
    loading ? "Searching…" : error ?? (response.state === "results" ? `${people.length} ${people.length === 1 ? "person" : "people"} found` : COPY[response.state]);

  return <div>
    <div className="relative">
      <input {...wire(w)} aria-describedby={[w.describedBy, statusId].filter(Boolean).join(" ")} className="input" type="text"
        role="combobox" aria-autocomplete="list" aria-expanded={expanded} aria-controls={expanded ? listId : undefined}
        aria-activedescendant={expanded && active >= 0 && people[active] ? optionId(active) : undefined}
        autoComplete="off" disabled={disabled} value={value?.displayName ?? query}
        onFocus={() => setOpen(true)} onBlur={() => setOpen(false)}
        onChange={(e) => {
          generation.current++;
          setQuery(e.target.value);
          setResponse({ state: "short_query", people: [] });
          setError(null);
          setActive(-1);
          setOpen(true);
          onChange(null);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            setOpen(true);
            setActive((i) => Math.max(0, Math.min(people.length - 1, i + (e.key === "ArrowDown" ? 1 : -1))));
          } else if (e.key === "Enter" && expanded) {
            e.preventDefault();
            if (people[active]) pick(people[active]);
          } else if (e.key === "Escape") {
            e.preventDefault();
            setOpen(false);
            setActive(-1);
          }
        }} />
      {expanded && <ul id={listId} role="listbox" aria-label="People" className="mt-1 max-h-72 overflow-y-auto rounded-md border border-border-strong bg-surface py-1 shadow-lg">
        {people.map((person, i) => <li key={person.selectionId} id={optionId(i)} role="option" aria-selected={active === i}
          className="listbox-option min-h-11 gap-3" onMouseDown={(e) => e.preventDefault()} onMouseMove={() => setActive(i)} onClick={() => pick(person)}>
          {!person.teen && person.avatarUrl && <Avatar url={person.avatarUrl} name={person.displayName} focalX={person.avatarFocalX} focalY={person.avatarFocalY} />}
          <span className="min-w-0 grow break-words">
            <span className="font-semibold">{person.displayName}</span>{person.teen && <> <Badge>Teen</Badge></>}
            {person.sharedGroups.length > 0 && <span className="block text-sm text-text-muted">Shared groups: {person.sharedGroups.join(", ")}</span>}
            {person.otherGroups.length > 0 && <span className="block text-sm text-text-muted">Other groups: {person.otherGroups.join(", ")}</span>}
          </span>
        </li>)}
      </ul>}
    </div>
    <p id={statusId} role="status" aria-live="polite" className="mt-2 text-sm text-text-muted">{status}</p>
    {duplicate && !value && <p className="mt-2 text-sm text-text-muted">More than one match? Ask an admin.</p>}
  </div>;
}
