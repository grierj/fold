import { expect, it } from "vitest";
import { placeLine, roomName } from "./place";

const hall = { id: 1, name: "Fellowship Hall", building: "Main" };

it("shows the room as the place, building after the name", () => {
  expect(placeLine(hall, "")).toBe("Fellowship Hall · Main");
  expect(roomName(hall)).toBe("Fellowship Hall · Main");
});

it("follows the room with a typed location as a detail", () => {
  expect(placeLine(hall, "north door")).toBe("Fellowship Hall · Main · north door");
});

it("leaves out an empty building", () => {
  expect(placeLine({ ...hall, building: "" }, "")).toBe("Fellowship Hall");
  expect(placeLine({ ...hall, building: "" }, "north door")).toBe("Fellowship Hall · north door");
});

it("is the typed location exactly as before when there is no room", () => {
  expect(placeLine(undefined, "Room 201")).toBe("Room 201");
  expect(placeLine(undefined, "")).toBe("");
  expect(placeLine(null, undefined)).toBe("");
});

it("doesn't repeat a typed location that is just the room (M2.1e)", () => {
  expect(placeLine(hall, " fellowship hall ")).toBe("Fellowship Hall · Main");
  expect(placeLine(hall, "Fellowship Hall · Main")).toBe("Fellowship Hall · Main");
  expect(placeLine(hall, "Fellowship Hall annex")).toBe("Fellowship Hall · Main · Fellowship Hall annex");
});
