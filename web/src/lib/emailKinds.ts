import type { EmailKind } from "../types";

// What each optional kind of email is called, with a line about what it is
// (DEC-115). The words match the email footer ("You're getting group updates
// …"). The order is the order the server lists them in.
export const EMAIL_KIND_COPY: Record<EmailKind["category"], { label: string; hint: string }> = {
  groups: { label: "Group updates", hint: "When your groups change or are approved" },
  events: { label: "Event updates", hint: "Cancelled or restored dates you can see" },
  messages: { label: "Messages from leaders", hint: "Announcements from your group leaders, event hosts and church admins" },
  admin: { label: "Admin notices", hint: "For church admins" },
};
