import assert from "node:assert/strict";
import { test } from "node:test";
import { Children, isValidElement, type ReactElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { Table } from "./ui";

type KeyInput = {
  key: string; target: object; currentTarget: object; repeat: boolean;
  nativeEvent: { isComposing: boolean }; preventDefault: () => void;
};
type RowProps = { children?: ReactNode; tabIndex?: number; onClick?: () => void; onKeyDown?: (event: KeyInput) => void };
const rows = [{ id: "history-1" }, { id: "history-2" }];
const cols = [{ key: "id", label: "Saved record", render: (row: { id: string }) => row.id }];
function bodyRows(node: ReactNode): ReactElement<RowProps>[] {
  return Children.toArray(node).flatMap(child => {
    if (!isValidElement<{ children?: ReactNode }>(child)) return [];
    if (child.type === "tbody") return Children.toArray(child.props.children).filter(isValidElement<RowProps>);
    return bodyRows(child.props.children);
  });
}
function input(key: string, overrides: Partial<KeyInput> = {}) {
  const target = {}, state = { prevented: 0 };
  return { event: { key, target, currentTarget: target, repeat: false, nativeEvent: { isComposing: false },
    preventDefault: () => { state.prevented++; }, ...overrides }, state };
}
test("clickable saved rows enter the tab order and Enter opens the exact selected record once", () => {
  const selected: string[] = [];
  const rendered = bodyRows(Table({ rows, cols, rowKey: row => row.id, onRow: row => selected.push(row.id) }));
  assert.deepEqual(rendered.map(row => row.props.tabIndex), [0, 0]);
  const event = input("Enter");
  rendered[1]?.props.onKeyDown?.(event.event);
  assert.deepEqual(selected, ["history-2"]);
  assert.equal(event.state.prevented, 1);
});
test("Space opens the focused saved row and prevents page scrolling", () => {
  const selected: string[] = [];
  const row = bodyRows(Table({ rows, cols, rowKey: row => row.id, onRow: row => selected.push(row.id) }))[0];
  const event = input(" ");
  row?.props.onKeyDown?.(event.event);
  assert.deepEqual(selected, ["history-1"]);
  assert.equal(event.state.prevented, 1);
});
test("child control keyboard events do not activate the enclosing saved row", () => {
  let opened = 0;
  const row = bodyRows(Table({ rows, cols, rowKey: row => row.id, onRow: () => { opened++; } }))[0];
  assert.equal(typeof row?.props.onKeyDown, "function");
  for (const key of ["Enter", " "]) {
    const event = input(key, { target: {} });
    row?.props.onKeyDown?.(event.event);
    assert.equal(event.state.prevented, 0);
  }
  assert.equal(opened, 0);
});
test("navigation keys and composition do not activate saved records", () => {
  let opened = 0;
  const row = bodyRows(Table({ rows, cols, rowKey: row => row.id, onRow: () => { opened++; } }))[0];
  assert.equal(typeof row?.props.onKeyDown, "function");
  for (const event of [input("Tab"), input("Escape"), input("ArrowDown"), input("Enter", { nativeEvent: { isComposing: true } })]) {
    row?.props.onKeyDown?.(event.event);
    assert.equal(event.state.prevented, 0);
  }
  assert.equal(opened, 0);
});
test("holding an activation key does not reopen the same saved record", () => {
  let opened = 0;
  const row = bodyRows(Table({ rows, cols, rowKey: row => row.id, onRow: () => { opened++; } }))[0];
  assert.equal(typeof row?.props.onKeyDown, "function");
  row?.props.onKeyDown?.(input("Enter").event);
  row?.props.onKeyDown?.(input("Enter", { repeat: true }).event);
  assert.equal(opened, 1);
});
test("mouse activation keeps the existing record callback", () => {
  const selected: string[] = [];
  const row = bodyRows(Table({ rows, cols, rowKey: row => row.id, onRow: item => selected.push(item.id) }))[1];
  row?.props.onClick?.();
  assert.deepEqual(selected, ["history-2"]);
});
test("static tables and empty rows add no focus or activation handlers", () => {
  for (const node of [Table({ rows, cols, rowKey: row => row.id }), Table({ rows: [], cols, rowKey: row => row.id, onRow: () => { throw new Error("empty row activated"); } })]) {
    const rendered = bodyRows(node);
    assert.ok(rendered.length > 0);
    for (const row of rendered) {
      assert.equal(row.props.tabIndex, undefined);
      assert.equal(row.props.onKeyDown, undefined);
      assert.equal(row.props.onClick, undefined);
    }
    assert.ok(!renderToStaticMarkup(node).includes("tabindex"));
  }
});
test("keyboard support preserves table semantics, content and existing visual markup", () => {
  const interactive = renderToStaticMarkup(Table({ rows, cols, rowKey: row => row.id, onRow: () => undefined }));
  const baseline = interactive.replaceAll(' tabindex="0"', "");
  const readOnly = renderToStaticMarkup(Table({ rows, cols, rowKey: row => row.id }));
  assert.equal((interactive.match(/tabindex="0"/g) ?? []).length, 2);
  assert.equal(baseline.replaceAll(" cursor-pointer hover:bg-surface-2/60", ""), readOnly);
  assert.ok(!interactive.includes('role="button"'));
});
