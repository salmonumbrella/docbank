import { afterEach, expect, it, vi } from "vite-plus/test";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import PersonChip from "./PersonChip.svelte";

afterEach(cleanup);

it("names the button by the visible name and selects the person", async () => {
  const onselect = vi.fn();
  render(PersonChip, { person: { id: "p1", name: "Mara Okonkwo-Fell", colorKey: "u1" }, onselect });
  const chip = screen.getByRole("button", { name: "Mara Okonkwo-Fell" });
  await fireEvent.click(chip);
  expect(onselect).toHaveBeenCalledWith("p1");
  expect(screen.queryByRole("img")).toBeNull();
});
