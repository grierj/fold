import { memo, useId } from "react";
import type { ErasureReasonItem } from "../types";
import { formatShortDay } from "../lib/when";
import { Badge } from "./ui/Badge";
import { Choices, Radio } from "./ui/Field";

// The contact-request reasons an admin must decide about before an account is
// deleted (T07B-2c). Shared with the deleted-accounts review (T07B-3b), so it
// knows nothing about deleting: it shows the items, takes one explicit choice
// per item and reports each choice. There is no bulk control and nothing is
// ever preselected: an item with no entry in `choices` has no radio checked.

export type ReasonChoice = "keep" | "remove";

export const CHANGED_COPY = "Changed since you looked: choose again";
const NOT_CHOSEN_COPY = "Not chosen yet";
const PARTY_COPY = "You're part of this request";
const OCCURRENCE_ADD_NOTE = "This reason wasn't shown to the people who decided this request.";

const PURPOSES: Record<string, string> = {
  contact: "Request for contact details",
  group_add: "Request to add to a group",
  occurrence_add: "Request to sign up for a date",
};
const STATUSES: Record<string, string> = {
  pending: "Waiting for a decision",
  granted: "Approved",
  denied: "Denied",
  withdrawn: "Withdrawn",
  expired: "Expired",
  ended: "Ended",
};

// A deleted counterpart is never named, only told apart by the reference of
// their deleted account (R4-C07). The server already sends "Deleted account".
function counterpartLabel(item: ErasureReasonItem): string {
  if (!item.counterpartDeleted) return item.counterpartName;
  return item.counterpartRef ? `Deleted account · ref ${item.counterpartRef}` : "Deleted account";
}

function whoDid(item: ErasureReasonItem): string {
  const other = counterpartLabel(item);
  return item.role === "requester" ? `They made this request about ${other}.` : `${other} made this request about them.`;
}

type Props = {
  items: ErasureReasonItem[];
  // The choice made so far, by request id. Missing means not chosen yet.
  choices: Readonly<Record<string, ReasonChoice | undefined>>;
  // Request ids whose item is new or different since the admin last looked.
  changed?: ReadonlySet<string>;
  onChoose: (item: ErasureReasonItem, choice: ReasonChoice) => void;
  disabled?: boolean;
  // A list that shows one display page of a longer review (the deletion panel)
  // says where it stands: items[0] is reason firstOrdinal + 1 of total. Without
  // them the list is the whole review, as T07B-3b uses it.
  firstOrdinal?: number;
  total?: number;
};

type RowProps = {
  item: ErasureReasonItem;
  chosen: ReasonChoice | undefined;
  isChanged: boolean;
  ordinal: number;
  total: number;
  group: string;
  disabled: boolean;
  onChoose: (item: ErasureReasonItem, choice: ReasonChoice) => void;
};

// One reason. Memoised on primitives plus the item (whose identity never changes
// once its page is final) and a stable onChoose, so a click re-renders its own
// row and not the other forty-nine.
const ReasonReviewRow = memo(function ReasonReviewRow({ item, chosen, isChanged, ordinal, total, group, disabled, onChoose }: RowProps) {
  const status = STATUSES[item.status];
  return (
    <li
      data-reason-id={item.requestId}
      data-changed={isChanged ? "true" : undefined}
      className="space-y-2 rounded-md border border-border bg-surface p-3 text-text"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span tabIndex={-1} data-reason-heading={ordinal} className="font-semibold">
          {PURPOSES[item.purpose] ?? "Request"}
        </span>
        {item.reviewerIsParty && <Badge>{PARTY_COPY}</Badge>}
        {chosen === undefined && !isChanged && <Badge>{NOT_CHOSEN_COPY}</Badge>}
        {isChanged && <Badge>{CHANGED_COPY}</Badge>}
      </div>
      <p className="wrap-anywhere text-text-muted">
        {whoDid(item)}
        {status ? ` ${status}.` : ""} Sent {formatShortDay(item.createdAt)}.
        {item.contextLabel ? ` ${item.contextLabel}.` : ""}
      </p>
      <div>
        <p className="font-semibold text-text-muted">Reason</p>
        <p className="whitespace-pre-wrap wrap-anywhere rounded-sm bg-sunken p-2">{item.reason}</p>
      </div>
      {item.purpose === "occurrence_add" && <p className="text-text-muted">{OCCURRENCE_ADD_NOTE}</p>}
      <Choices legend={`Reason ${ordinal + 1} of ${total}`}>
        <Radio
          name={`${group}-${item.requestId}`}
          value="keep"
          label="Keep text"
          checked={chosen === "keep"}
          disabled={disabled}
          onChange={() => onChoose(item, "keep")}
        />
        <Radio
          name={`${group}-${item.requestId}`}
          value="remove"
          label="Blank text"
          checked={chosen === "remove"}
          disabled={disabled}
          onChange={() => onChoose(item, "remove")}
        />
      </Choices>
    </li>
  );
});

export function ReasonReviewList({ items, choices, changed, onChoose, disabled, firstOrdinal = 0, total }: Props) {
  const group = useId();
  return (
    <ul aria-label="Contact-request reasons" className="space-y-3">
      {items.map((item, i) => {
        const chosen = choices[item.requestId];
        return (
          <ReasonReviewRow
            key={item.requestId}
            item={item}
            chosen={chosen}
            isChanged={changed?.has(item.requestId) === true && chosen === undefined}
            ordinal={firstOrdinal + i}
            total={total ?? items.length}
            group={group}
            disabled={disabled === true}
            onChoose={onChoose}
          />
        );
      })}
    </ul>
  );
}
