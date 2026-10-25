// These optional fields are already authorized by the API. Missing fields
// must stay missing; a name or relationship is not permission to fill them in.
export function ContactDetails({ email, phone }: { email?: string; phone?: string }) {
  if (!email && !phone) return null;
  return <div className="text-sm text-text-muted wrap-anywhere">
    {email && <div>Email: {email}</div>}
    {phone && <div>Phone: {phone}</div>}
  </div>;
}
