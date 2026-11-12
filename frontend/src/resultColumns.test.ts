import { expect, it } from "vite-plus/test";
import { resultColumns } from "./resultColumns.js";

const search = { match: true, actions: false };

it("drops Type, then Match, then Modified as the table narrows", () => {
  expect(resultColumns(700, search)).toMatchObject({ modified: true, match: true, type: true });
  expect(resultColumns(600, search)).toMatchObject({ modified: true, match: true, type: false });
  expect(resultColumns(520, search)).toMatchObject({ modified: true, match: false, type: false });
  expect(resultColumns(400, search)).toMatchObject({ modified: false, match: false, type: false });
});

it("reserves room for an actions column", () => {
  expect(resultColumns(520, { match: false, actions: true }).modified).toBe(false);
});

it("only adds facts when both columns fit and the region is wide enough", () => {
  expect(resultColumns(899, { match: false, actions: false }).facts).toBe(false);
  expect(resultColumns(900, search).facts).toBe(false);
  expect(resultColumns(1400, search).facts).toBe(true);
});
