import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { ImageUpload } from "./ImageUpload";

const hint = "Tap or click the image to move the focal point — faces stay centered when cropped.";
const props = { label: "Cover image", initialFocal: { x: 50, y: 50 }, maxDim: 1600, aspectClass: "aspect-video", onFile: vi.fn(), onFocal: vi.fn() };

beforeEach(() => {
  props.onFile.mockReset();
  props.onFocal.mockReset();
  vi.stubGlobal("URL", class extends URL {
    static createObjectURL = vi.fn(() => "blob:chosen");
    static revokeObjectURL = vi.fn();
  });
});

it("is a compact row with no image: a 64px thumbnail of the placeholder and Choose photo, no crop box or hint", () => {
  const { container } = render(<ImageUpload {...props} placeholderSrc="/patterns/one.svg" />);
  const thumb = container.querySelector("img")!;
  expect(thumb).toHaveAttribute("src", "/patterns/one.svg");
  expect(thumb).toHaveClass("w-16", "h-16");
  expect(screen.getByRole("button", { name: "Choose photo" })).toBeInTheDocument();
  expect(screen.queryByText(hint)).not.toBeInTheDocument();
  expect(screen.queryByText("No image")).not.toBeInTheDocument();
  expect(container.querySelector("[title='Click to set the focal point']")).toBeNull();
  expect(screen.queryByRole("button", { name: "Clear" })).not.toBeInTheDocument();
});

it("falls back to the person's initials, or a blank square when it has neither", () => {
  const view = render(<ImageUpload {...props} label="Profile picture" placeholderName="pat" />);
  expect(screen.getByText("P")).toBeInTheDocument();
  expect(view.container.querySelector("img")).toBeNull();
  view.unmount();
  const blank = render(<ImageUpload {...props} />);
  expect(blank.container.querySelector("img")).toBeNull();
  expect(screen.getByRole("button", { name: "Choose photo" })).toBeInTheDocument();
});

it("shows the crop box, its hint and the usual buttons when an image already exists", async () => {
  const { container } = render(<ImageUpload {...props} initialUrl="/uploads/cover.jpg" placeholderSrc="/patterns/one.svg" />);
  expect(screen.getByText(hint)).toBeInTheDocument();
  expect(container.querySelector("img")).toHaveAttribute("src", "/uploads/cover.jpg");
  expect(screen.getByRole("button", { name: "Choose different image" })).toBeInTheDocument();
  await userEvent.click(container.querySelector("[title='Click to set the focal point']")!);
  expect(props.onFocal).toHaveBeenCalledOnce();
});

it("shows the crop box only once a file is chosen, and Clear goes back to the row", async () => {
  const { container } = render(<ImageUpload {...props} placeholderSrc="/patterns/one.svg" />);
  const input = container.querySelector<HTMLInputElement>("input[type=file]")!;
  const file = new File(["x"], "face.png", { type: "image/png" });
  await userEvent.upload(input, file);
  await waitFor(() => expect(props.onFile).toHaveBeenCalledWith(file));
  expect(await screen.findByText(hint)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Choose different image" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Clear" }));
  expect(props.onFile).toHaveBeenLastCalledWith(null);
  expect(screen.queryByText(hint)).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Choose photo" })).toBeInTheDocument();
});
