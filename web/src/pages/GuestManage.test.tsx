import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { Guests } from "../api";
import { GuestManage } from "./GuestManage";
vi.mock("../api", async (actual) => ({ ...await actual<typeof import("../api")>(), Guests: { getManage: vi.fn() } }));
it.each(["going", "cancelled"] as const)("keeps a %s guest's navigation on the one date", async (status) => {
  vi.mocked(Guests.getManage).mockResolvedValue({ registration: { eventId: 7, occurrenceId: 51, date: "2026-12-01", title: "Welcome evening", hostedBy: "Church", status, startsAt: "2026-12-01T19:00:00Z", location: "" } });
  render(<MemoryRouter initialEntries={["/guest/manage/token"]}><Routes><Route path="/guest/manage/:token" element={<GuestManage />} /></Routes></MemoryRouter>);
  expect(await screen.findByRole("link", { name: status === "going" ? "View the event" : "Changed your mind? Register again" })).toHaveAttribute("href", "/events/7/dates/2026-12-01");
});
