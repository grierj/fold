import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { VideoField, emptyVideo, videoProblem, type VideoValue } from "./VideoField";

const generateJitsi = vi.fn().mockResolvedValue({ url: "https://meet.jit.si/fold-abc123def4" });

vi.mock("../api", () => ({
  ApiError: class extends Error {},
  Events: { generateJitsi: () => generateJitsi() },
}));

function renderField(initial: VideoValue = emptyVideo) {
  let value = initial;
  const onChange = vi.fn((next: VideoValue) => {
    value = next;
  });
  const view = render(<VideoField idPrefix="test" value={initial} onChange={onChange} />);
  return {
    onChange,
    getValue: () => value,
    rerender: () => view.rerender(<VideoField idPrefix="test" value={value} onChange={onChange} />),
  };
}

describe("VideoField", () => {
  it("starts with no video link and hides the link and reveal fields", () => {
    renderField();
    expect(screen.getByLabelText(/^No video link/)).toBeChecked();
    expect(screen.queryByLabelText("Video link")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Who sees the link")).not.toBeInTheDocument();
  });

  it("pasting a link asks who sees it, defaulting to registered people only", async () => {
    const f = renderField();
    await userEvent.click(screen.getByLabelText(/^Paste a link/));
    expect(f.getValue().provider).toBe("link");
    f.rerender();
    const input = screen.getByLabelText("Video link") as HTMLInputElement;
    await userEvent.click(input);
    await userEvent.paste("https://zoom.example.org/j/1");
    expect(f.getValue().url).toContain("https://zoom.example.org/j/1");
    const reveal = screen.getByLabelText("Who sees the link") as HTMLSelectElement;
    expect(reveal.value).toBe("registered_only");
    expect(screen.getByRole("option", { name: "Registered people only" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Everyone who can see the event" })).toBeInTheDocument();
  });

  it("generates a Jitsi link into the field", async () => {
    const f = renderField({ provider: "jitsi", url: "", reveal: "registered_only" });
    await userEvent.click(screen.getByRole("button", { name: "Generate Jitsi link" }));
    expect(generateJitsi).toHaveBeenCalled();
    expect(f.getValue()).toEqual({
      provider: "jitsi",
      url: "https://meet.jit.si/fold-abc123def4",
      reveal: "registered_only",
    });
  });

  it("carries an existing link", () => {
    renderField({ provider: "link", url: "https://meet.example.org/room", reveal: "always" });
    expect((screen.getByLabelText("Video link") as HTMLInputElement).value).toBe(
      "https://meet.example.org/room"
    );
    expect((screen.getByLabelText("Who sees the link") as HTMLSelectElement).value).toBe("always");
  });
});

describe("VideoField errors", () => {
  it("shows the form's error on the link input", () => {
    render(
      <VideoField
        idPrefix="err"
        value={{ provider: "link", url: "", reveal: "registered_only" }}
        onChange={() => {}}
        error="Paste the video link, or pick No video link."
      />
    );
    const input = screen.getByLabelText("Video link");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input.getAttribute("aria-describedby")).toContain("video-url-err-error");
    expect(input.id).toBe("video-url-err");
  });

  it("checks for a full https:// link before submit", () => {
    expect(videoProblem(emptyVideo)).toBeNull();
    expect(videoProblem({ provider: "link", url: "", reveal: "always" })).toMatch(/Paste the video link/);
    expect(videoProblem({ provider: "link", url: "http://x.example", reveal: "always" })).toMatch(/https:\/\//);
    expect(videoProblem({ provider: "link", url: "https://x.example/r", reveal: "always" })).toBeNull();
  });
});
