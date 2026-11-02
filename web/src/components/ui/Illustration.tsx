import emptyGroups from "../../assets/illustrations/empty-groups.svg";
import allCaughtUp from "../../assets/illustrations/all-caught-up.svg";
import notFound from "../../assets/illustrations/not-found.svg";
import cantLoad from "../../assets/illustrations/cant-load.svg";
import requestSent from "../../assets/illustrations/request-sent.svg";
import linkExpired from "../../assets/illustrations/link-expired.svg";
import noEvents from "../../assets/illustrations/no-events.svg";
import firstGroup from "../../assets/illustrations/first-group.svg";
import emptyAlerts from "../../assets/illustrations/empty-alerts.svg";
import emptyMessages from "../../assets/illustrations/empty-messages.svg";
import emptyMyGroups from "../../assets/illustrations/empty-my-groups.svg";

export type IllustrationName =
  | "empty-groups"
  | "all-caught-up"
  | "not-found"
  | "cant-load"
  | "request-sent"
  | "link-expired"
  | "no-events"
  | "first-group"
  | "empty-alerts"
  | "empty-messages"
  | "empty-my-groups";

const SOURCES: Record<IllustrationName, string> = {
  "empty-groups": emptyGroups,
  "all-caught-up": allCaughtUp,
  "not-found": notFound,
  "cant-load": cantLoad,
  "request-sent": requestSent,
  "link-expired": linkExpired,
  "no-events": noEvents,
  "first-group": firstGroup,
  "empty-alerts": emptyAlerts,
  "empty-messages": emptyMessages,
  "empty-my-groups": emptyMyGroups,
};

export function Illustration({ name, size = "md" }: { name: IllustrationName; size?: "sm" | "md" }) {
  return (
    <img
      src={SOURCES[name]}
      alt=""
      aria-hidden="true"
      width={240}
      height={180}
      decoding="async"
      className={size === "sm" ? "w-16 h-auto" : "w-30 sm:w-40 h-auto"}
    />
  );
}
