import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, expect, it, vi } from "vitest";
import { ContactHistory } from "../contact-history-api";
import type { ContactHistoryPeopleResponse, ContactHistoryPerson } from "../types";
import { ContactHistoryPersonPicker } from "./ContactHistoryPersonPicker";

vi.mock("../contact-history-api", async (actual) => ({
  ...await actual<typeof import("../contact-history-api")>(),
  ContactHistory: { people: vi.fn() },
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function ControlledPicker({ initialValue = "", onKnown, onFailure }: {
  initialValue?: string;
  onKnown: (person: ContactHistoryPerson) => void;
  onFailure: (error: unknown) => void;
}) {
  const [value, setValue] = useState(initialValue);
  return <ContactHistoryPersonPicker value={value} name={undefined} error="" onChange={setValue} onKnown={onKnown} onFailure={onFailure} />;
}

beforeEach(() => {
  vi.mocked(ContactHistory.people).mockReset();
});

it("keeps long choices complete while showing a compact selection and full identity", async () => {
  const selected = { id: "353", name: "Harper History with a deliberately long public name", email: "history-proof-with-a-long-address@example.com" };
  const other = { id: "354", name: "Another Long Person", email: "another-long-address@example.com" };
  vi.mocked(ContactHistory.people).mockResolvedValue({ people: [selected, other] });
  render(<ControlledPicker onKnown={vi.fn()} onFailure={vi.fn()} />);

  const fullOther = `${other.name} — ${other.email}`;
  await screen.findByRole("option", { name: `${selected.name} — ${selected.email}` });
  await userEvent.selectOptions(screen.getByLabelText("Person"), selected.id);

  const select = screen.getByLabelText("Person") as HTMLSelectElement;
  expect(select).toHaveValue(selected.id);
  expect(select.selectedOptions[0]).toHaveTextContent("Selected person");
  expect(screen.getByRole("option", { name: fullOther })).toBeInTheDocument();
  const fullName = screen.getByText(selected.name, { selector: "span" });
  expect(fullName.closest(".wrap-anywhere")).not.toBeNull();
  expect(screen.getByText(selected.email, { selector: "span" })).toBeVisible();
  expect(select).toHaveAccessibleDescription(`${selected.name} ${selected.email}`);
});

it("uses a compact saved-ID option and shows the full retained ID beside it", () => {
  const id = "9223372036854775807";
  vi.mocked(ContactHistory.people).mockResolvedValue({ people: [] });
  render(<ControlledPicker initialValue={id} onKnown={vi.fn()} onFailure={vi.fn()} />);

  const select = screen.getByLabelText("Person") as HTMLSelectElement;
  expect(select.selectedOptions[0]).toHaveTextContent("Saved person ID");
  const identity = screen.getByText(`Saved person ID: ${id}`, { exact: true });
  expect(identity.closest(".wrap-anywhere")).not.toBeNull();
  expect(select).toHaveAccessibleDescription(`Saved person ID: ${id}`);
});

it("ignores a late saved-ID lookup after the selected ID changes", async () => {
  const oldLookup = deferred<ContactHistoryPeopleResponse>();
  const currentLookup = deferred<ContactHistoryPeopleResponse>();
  vi.mocked(ContactHistory.people).mockImplementation(({ id }) => {
    if (id === "9007199254740992") return oldLookup.promise;
    if (id === "9007199254740993") return currentLookup.promise;
    return Promise.resolve({ people: [] });
  });
  const onKnown = vi.fn();
  const onFailure = vi.fn();
  const props = { name: undefined, error: "", onChange: vi.fn(), onKnown, onFailure };
  const view = render(<ContactHistoryPersonPicker {...props} value="9007199254740992" />);
  await waitFor(() => expect(ContactHistory.people).toHaveBeenCalledWith({ id: "9007199254740992" }));

  view.rerender(<ContactHistoryPersonPicker {...props} value="9007199254740993" />);
  await waitFor(() => expect(ContactHistory.people).toHaveBeenCalledWith({ id: "9007199254740993" }));

  await act(async () => currentLookup.resolve({ people: [{ id: "9007199254740993", name: "Current Person", email: "current@example.com" }] }));
  await waitFor(() => expect(onKnown).toHaveBeenCalledWith({ id: "9007199254740993", name: "Current Person", email: "current@example.com" }));

  await act(async () => oldLookup.resolve({ people: [{ id: "9007199254740992", name: "Old Person", email: "old@example.com" }] }));
  expect(onKnown).not.toHaveBeenCalledWith(expect.objectContaining({ id: "9007199254740992" }));
  expect(onFailure).not.toHaveBeenCalled();
});

it("tells the page when a person is chosen from the list, but not when the ID box is typed in", async () => {
  vi.mocked(ContactHistory.people).mockResolvedValue({ people: [{ id: "353", name: "Harper History", email: "harper@example.com" }] });
  const onPick = vi.fn();
  const Wrapper = () => {
    const [value, setValue] = useState("");
    return <ContactHistoryPersonPicker value={value} name={undefined} error="" onChange={setValue} onPick={onPick} onKnown={vi.fn()} onFailure={vi.fn()} />;
  };
  render(<Wrapper />);
  await screen.findByRole("option", { name: "Harper History — harper@example.com" });
  await userEvent.click(screen.getByText("Look up a deleted account"));
  await userEvent.type(screen.getByLabelText("Saved person ID"), "12");
  expect(onPick).not.toHaveBeenCalled();
  await userEvent.selectOptions(screen.getByLabelText("Person"), "353");
  expect(onPick).toHaveBeenCalledWith("353");
});
