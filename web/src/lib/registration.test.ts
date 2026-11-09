import { describe, expect, it } from "vitest";
import { occurrence } from "../test/occurrence";
import { offersRemote, poolFull, registerBody, savedAnswer, savedAnswerFromState } from "./registration";

const answer = { status: "going" as const, attendanceMode: "in_person" as const, guestCount: 2 };

describe("registerBody", () => {
  it("sends guests only with going, and the mode only when remote is offered", () => {
    const e = occurrence({ remoteAvailable: false });
    expect(registerBody(e, answer)).toMatchObject({ status: "going", guestCount: 2, attendanceMode: undefined });
    expect(registerBody(occurrence({ remoteAvailable: true }), { ...answer, attendanceMode: "remote" })).toMatchObject({ attendanceMode: "remote" });
    expect(registerBody(e, { ...answer, status: "maybe" }).guestCount).toBe(0);
  });

  it("asks for no childcare on a first going answer, and leaves a standing request alone", () => {
    expect(registerBody(occurrence({ childcareAvailable: true, myRegistration: null }), answer)).toMatchObject({ childcareCount: 0, childcareAgesNote: "" });
    expect(registerBody(occurrence({ childcareAvailable: true, myRegistration: "going" }), answer)).not.toHaveProperty("childcareCount");
    expect(registerBody(occurrence({ childcareAvailable: true }), { ...answer, childcare: { childcareCount: 2, childcareAgesNote: "3 and 5" } })).toMatchObject({ childcareCount: 2 });
    expect(registerBody(occurrence({ childcareAvailable: true }), { ...answer, status: "maybe" })).not.toHaveProperty("childcareCount");
    expect(registerBody(occurrence({ childcareAvailable: false }), answer)).not.toHaveProperty("childcareCount");
  });
});

describe("seats", () => {
  it("treats a null cap as unlimited and a missing count as one seat left", () => {
    expect(poolFull(occurrence({ capacity: null, spotsLeft: 0 }), "in_person")).toBe(false);
    expect(poolFull(occurrence({ capacity: 5, spotsLeft: null }), "in_person")).toBe(false);
    expect(poolFull(occurrence({ capacity: 5, spotsLeft: 0 }), "in_person")).toBe(true);
    expect(poolFull(occurrence({ capacity: 5, spotsLeft: 3, remoteCapacity: 2, remoteSpotsLeft: 0 }), "remote")).toBe(true);
    expect(poolFull(occurrence({ capacity: 5, spotsLeft: 3, remoteCapacity: 2, remoteSpotsLeft: 0 }), "in_person")).toBe(false);
  });

  it("offers remote only on a remote event with seats", () => {
    expect(offersRemote(occurrence({ remoteAvailable: true, remoteCapacity: null }))).toBe(true);
    expect(offersRemote(occurrence({ remoteAvailable: true, remoteCapacity: 0 }))).toBe(false);
    expect(offersRemote(occurrence({ remoteAvailable: false }))).toBe(false);
  });
});

describe("saved answer", () => {
  it("reads the same defaults from the date and from a server result", () => {
    expect(savedAnswer(occurrence({ myRegistration: null }))).toEqual(savedAnswerFromState({}));
    expect(savedAnswerFromState({ status: "going", guestCount: 1, childcareCount: 2, childcareAgesNote: "4", attendanceMode: "remote" }))
      .toEqual({ status: "going", mode: "remote", guests: 1, children: 2, ages: "4" });
    expect(savedAnswerFromState({ status: "maybe" })).toEqual({ status: "maybe", mode: "in_person", guests: 0, children: 0, ages: "" });
  });
});
