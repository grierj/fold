// Same rule as the server (password_rules.go): the 10 minimum counts
// characters (code points, so an emoji is one), the 72 maximum counts UTF-8
// bytes because that is bcrypt's limit. Never trims the password.
function passwordLengthProblem(pw: string): "short" | "long" | null {
  if ([...pw].length < 10) return "short";
  if (new TextEncoder().encode(pw).length > 72) return "long";
  return null;
}

export const PASSWORD_HINT = "Must be at least 10 characters, and at most 72 bytes (accents and emoji take more).";

// The sentence for a password that breaks the rule, or null when it is fine.
export function passwordProblem(pw: string): string | null {
  const problem = passwordLengthProblem(pw);
  if (problem === "short") return "Password must be at least 10 characters.";
  if (problem === "long") return "That password is too long. Use at most 72 bytes — about 72 letters, fewer with accents or emoji.";
  return null;
}
