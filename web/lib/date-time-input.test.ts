import assert from "node:assert/strict";
import { test } from "node:test";
import { pickerDateTime, pickerTimeToUTC } from "./date-time-input";

test("date picker interprets wall time in the indicated zone", () => {
  assert.equal(pickerTimeToUTC("2026-11-14T08:00:01", "Asia/Taipei"), "2026-11-14T00:00:01.000Z");
  assert.equal(pickerTimeToUTC("2026-11-14T00:00", "UTC"), "2026-11-14T00:00:00.000Z");
  assert.equal(pickerDateTime("2026-11-14T00:00:01.123456789Z", "Asia/Taipei"), "2026-11-14T08:00:01");
});

test("date picker rejects nonexistent, repeated and invalid local times", () => {
  assert.equal(pickerTimeToUTC("2026-03-08T02:30", "America/New_York"), null);
  assert.equal(pickerTimeToUTC("2026-11-01T01:30", "America/New_York"), null);
  assert.equal(pickerTimeToUTC("2026-11-01T02:30", "America/New_York"), "2026-11-01T07:30:00.000Z");
  assert.equal(pickerTimeToUTC("2026-02-30T12:00", "UTC"), null);
  assert.equal(pickerTimeToUTC("2026-04-05T01:45", "Australia/Lord_Howe"), null);
});
