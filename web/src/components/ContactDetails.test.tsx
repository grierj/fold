import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { ContactDetails } from "./ContactDetails";

it("renders no contact details or request controls when fields are omitted", () => {
  const { container } = render(<ContactDetails />);
  expect(container).toBeEmptyDOMElement();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});

it("renders only the supplied authorized contact fields", () => {
  const view = render(<ContactDetails email="pat@example.com" phone="208-555-0100" />);
  expect(screen.getByRole("link", { name: "pat@example.com" })).toHaveAttribute("href", "mailto:pat%40example.com");
  expect(screen.getByText("Phone: 208-555-0100")).toBeInTheDocument();
  view.rerender(<ContactDetails email="pat@example.com" />);
  expect(screen.getByRole("link", { name: "pat@example.com" })).toHaveClass("min-h-11", "break-all");
  expect(screen.queryByText(/Phone:/)).not.toBeInTheDocument();
  view.rerender(<ContactDetails phone="208-555-0100" />);
  expect(screen.queryByRole("link")).not.toBeInTheDocument();
  expect(screen.queryByText(/Email:|@/)).not.toBeInTheDocument();
  expect(screen.getByText("Phone: 208-555-0100")).toBeInTheDocument();
});

it("encodes an address rather than interpreting it as mail headers", () => {
  render(<ContactDetails email="pat?subject=hello@example.com" />);
  expect(screen.getByRole("link")).toHaveAttribute("href", "mailto:pat%3Fsubject%3Dhello%40example.com");
});

it("does not invent placeholders for blank contact values", () => {
  const { container } = render(<ContactDetails email="" phone="" />);
  expect(container).toBeEmptyDOMElement();
});

it("removes previously authorized contact details when new props omit them", () => {
  const view = render(<ContactDetails email="pat@example.com" phone="208-555-0100" />);
  view.rerender(<ContactDetails />);
  expect(view.container).toBeEmptyDOMElement();
});
