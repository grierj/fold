// Texts (PLAN 7.3, M3.4b): the composer's live counter. It is the same logic
// as internal/content/sms.go, and both run sms-cases.json. GSM-7 when every
// character is in the GSM 03.38 alphabet (160 per text, 153 per part),
// else UCS-2 (70, or 67 per part, counting UTF-16 units: one emoji is two).
// The church-name prefix and the STOP footer are added and counted.

const GSM_BASIC =
  "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà";
const GSM_EXTENDED = "^{}\\[~]|€\f";

export interface SmsCount {
  encoding: "GSM-7" | "UCS-2";
  units: number;
  segments: number;
}

/** countSms counts a text as it will be sent; a part never splits an
 * escape pair or a surrogate pair. */
export function countSms(text: string): SmsCount {
  const chars = Array.from(text);
  let sizes: number[] = [];
  let gsm = true;
  for (const c of chars) {
    if (GSM_BASIC.includes(c)) sizes.push(1);
    else if (GSM_EXTENDED.includes(c)) sizes.push(2);
    else {
      gsm = false;
      break;
    }
  }
  let single = 160;
  let part = 153;
  if (!gsm) {
    sizes = chars.map((c) => c.length);
    single = 70;
    part = 67;
  }
  const units = sizes.reduce((a, n) => a + n, 0);
  let segments = 0;
  if (units > 0 && units <= single) segments = 1;
  else if (units > single) {
    segments = 1;
    let used = 0;
    for (const n of sizes) {
      if (used + n > part) {
        segments++;
        used = 0;
      }
      used += n;
    }
  }
  return { encoding: gsm ? "GSM-7" : "UCS-2", units, segments };
}

const SMS_FOOTER = "Reply STOP to opt out";

/** framedSms is a text as it goes out: "First Baptist: " before it and the
 * STOP footer on its own line after it. */
export function framedSms(church: string, body: string): string {
  return `${church.trim()}: ${body}\n${SMS_FOOTER}`;
}

// Asking for money by text makes carriers treat it as charity traffic
// (DEC-3); these words and giving-page links set off the warning.
const DONATION_WORDS =
  /\b(give|gives|giving|donate|donates|donating|donation|donations|tithe|tithes|tithing|offering|offerings)\b/i;
const URL_RE = /\b((?:https?:\/\/)?(?:[a-z0-9-]+\.)+[a-z]{2,}(?:\/[^\s]*)?)/gi;
const GIVING_HOSTS = [
  "tithe.ly",
  "pushpay.com",
  "givelify.com",
  "easytithe.com",
  "paypal.me",
  "paypal.com",
  "venmo.com",
  "cash.app",
  "givebutter.com",
  "onrealm.org",
];
const GIVING_PATH = /(^|[^a-z])(give|giving|donate|donations?|tithes?|tithing|offerings?)([^a-z]|$)/i;

/** isDonationAsk: the text reads as asking for money (DEC-3). */
export function isDonationAsk(text: string): boolean {
  for (const m of text.match(URL_RE) ?? []) {
    let u: URL;
    try {
      u = new URL(m.includes("://") ? m : `https://${m}`);
    } catch {
      continue;
    }
    const host = u.hostname.toLowerCase();
    if (GIVING_HOSTS.some((h) => host === h || host.endsWith(`.${h}`))) return true;
    if (GIVING_PATH.test(u.pathname) || GIVING_PATH.test(host.split(".")[0])) return true;
  }
  return DONATION_WORDS.test(text);
}
