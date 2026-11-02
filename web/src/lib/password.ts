// Same rule as the server (password_rules.go): the 10 minimum counts
// characters (code points, so an emoji is one), the 72 maximum counts UTF-8
// bytes because that is bcrypt's limit. Never trims the password.
export function passwordLengthProblem(pw: string): "short" | "long" | null {
  if ([...pw].length < 10) return "short";
  if (new TextEncoder().encode(pw).length > 72) return "long";
  return null;
}
