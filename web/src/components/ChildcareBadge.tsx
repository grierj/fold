import { Badge } from "./ui/Badge";

export function ChildcareBadge({ count }: { count?: number }) {
  return <Badge>{count == null ? "Childcare" : `+${count} childcare`}</Badge>;
}
