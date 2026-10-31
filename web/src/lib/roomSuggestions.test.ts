import { describe, expect, it } from "vitest";
import { suggestedRooms } from "./roomSuggestions";

const rooms = [{ id: 1 }, { id: 2 }, { id: 3 }];
const s = (ids: number[], otherKinds = false) => ({
  rooms: ids.map((roomId, i) => ({ roomId, uses: i + 1, reason: `Used ${i + 1} times`, busyDates: 0 })),
  otherKinds,
  dates: 1,
});

describe("suggestedRooms", () => {
  it("puts the suggested rooms first, in order, and every other room after, once", () => {
    expect(suggestedRooms(rooms, s([3, 1]))).toEqual({
      label: "Suggested",
      suggested: [{ room: { id: 3 }, reason: "Used 1 times" }, { room: { id: 1 }, reason: "Used 2 times" }],
      others: [{ id: 2 }],
    });
  });

  it("labels a fallback list as other kinds of events", () => {
    expect(suggestedRooms(rooms, s([2], true))?.label).toBe("Used by other kinds of events");
  });

  it("skips suggestions for rooms the picker doesn't list, and repeats", () => {
    expect(suggestedRooms(rooms, s([9, 2, 2]))?.suggested.map((x) => x.room.id)).toEqual([2]);
  });

  it("is null with nothing to suggest, so the picker lists rooms as before", () => {
    expect(suggestedRooms(rooms, null)).toBeNull();
    expect(suggestedRooms(rooms, undefined)).toBeNull();
    expect(suggestedRooms(rooms, s([]))).toBeNull();
    expect(suggestedRooms(rooms, s([9]))).toBeNull();
  });
});
