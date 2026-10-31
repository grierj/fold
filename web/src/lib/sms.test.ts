import { describe, expect, it } from "vitest";
import cases from "./sms-cases.json";
import { countSms, framedSms, isDonationAsk } from "./sms";

interface Piece {
  text: string;
  times?: number;
}

const build = (p: Piece) => (p.times ? p.text.repeat(p.times) : p.text);

// The same cases internal/content/sms_test.go runs, so the live counter and
// the server agree.
describe("countSms", () => {
  for (const tc of cases.segments as (Piece & {
    name: string;
    church?: string;
    parts?: Piece[];
    encoding: string;
    units: number;
    segments: number;
  })[]) {
    it(tc.name, () => {
      let text = tc.parts ? tc.parts.map(build).join("") : build(tc);
      if (tc.church) text = framedSms(tc.church, text);
      expect(countSms(text)).toEqual({ encoding: tc.encoding, units: tc.units, segments: tc.segments });
    });
  }
});

describe("isDonationAsk", () => {
  for (const tc of cases.donation) {
    it(`${tc.warn ? "warns" : "doesn't warn"}: ${tc.text}`, () => {
      expect(isDonationAsk(tc.text)).toBe(tc.warn);
    });
  }
});
