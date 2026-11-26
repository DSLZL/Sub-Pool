import { join } from "node:path";
import { expect, test, type APIResponse, type Page, type Response } from "@playwright/test";
import { fixtureBearer, fixtureKey, startServices } from "./services";

const account = "Browser account";
const member = "Browser member";
const otherMember = "Unshared member";
const pool = "Browser pool";
const ordinary = "browser_reader";
const afterExport = "after_export";

type Services = Awaited<ReturnType<typeof startServices>>;
let app: Services;

const button = (page: Page, name: string) => page.getByRole("button", { name, exact: true });
const tab = (page: Page, name: string) => page.getByRole("tab", { name, exact: true });
const row = (page: Page, name: string) => page.getByRole("row").filter({ has: page.getByText(name, { exact: true }) });

async function json<T>(response: APIResponse | Response): Promise<T> {
  expect(response.status(), new URL(response.url()).pathname).toBe(200);
  return response.json();
}

async function fixtureRead<T>(path: string): Promise<T> {
  const response = await fetch(app.fixtureOrigin + "/api/v1/admin/" + path, {
    headers: { "x-api-key": fixtureKey }, signal: AbortSignal.timeout(5000),
  });
  expect(response.status, path).toBe(200);
  return response.json() as Promise<T>;
}

async function login(page: Page, name: string, password: string, entry = "/login") {
  await page.goto(app.origin + entry);
  await page.getByLabel("用户名", { exact: true }).fill(name);
  await page.getByLabel("密码", { exact: true }).fill(password);
  const [response] = await Promise.all([
    page.waitForResponse(value => value.url().endsWith("/api/v1/auth/login") && value.request().method() === "POST"),
    button(page, "登录").click(),
  ]);
  expect(response.status(), "Login response").toBe(200);
}

async function firstLogin(page: Page) {
  await login(page, "admin", app.initialPassword);
  await expect(page).toHaveURL(/\/change-password$/);
  await page.goto(app.origin + "/console/connection");
  await expect(page).toHaveURL(/\/change-password$/);
  expect((await page.request.get(app.origin + "/api/v1/connection")).status()).toBe(403);
  await page.getByLabel(/当前密码/).fill(app.initialPassword);
  await page.getByLabel("新密码", { exact: true }).fill(app.adminPassword);
  await page.getByLabel(/^确认新密码/).fill(app.adminPassword);
  await button(page, "修改密码").click();
  await expect(page).toHaveURL(/\/login$/);
  await login(page, "admin", app.adminPassword);
  await expect(page).toHaveURL(/\/console$/);
}

async function setSchedule(page: Page, enabled: boolean) {
  await tab(page, "采集设置").click();
  const schedule = page.getByRole("switch", { name: /^启用定时采集/ });
  const usage = page.getByRole("switch", { name: /^定时采集用量/ });
  const save = button(page, "保存");
  await expect(save).toBeEnabled();
  const toggleSchedule = await schedule.getAttribute("aria-checked") !== String(enabled);
  const toggleUsage = await usage.getAttribute("aria-checked") === "true";
  if (toggleSchedule) await schedule.click();
  if (toggleUsage) await usage.click();
  if (toggleSchedule || toggleUsage) {
    const [saved] = await Promise.all([
      page.waitForResponse(response => response.url().endsWith("/api/v1/monitoring/config/update") && response.request().method() === "POST"),
      save.click(),
    ]);
    expect(saved.status()).toBe(200);
    await expect(save).toBeEnabled();
  }
  await expect(schedule).toHaveAttribute("aria-checked", String(enabled));
  await expect(usage).toHaveAttribute("aria-checked", "false");
  const policy = await json<object>(await page.request.get(app.origin + "/api/v1/monitoring/config"));
  expect(policy).toMatchObject({ enabled, usage_enabled: false });
}

async function memberForm(page: Page, name: string, email: string) {
  await button(page, "新建参与者").click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("名称", { exact: true }).fill(name);
  await dialog.getByRole("button", { name: email }).click();
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(row(page, name)).toBeVisible();
}

async function userForm(page: Page, username: string) {
  await button(page, "新建用户").click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("用户名", { exact: true }).fill(username);
  await dialog.getByLabel(/^初始密码/).fill(app.userPassword);
  return dialog;
}

function dates(date: string) {
  const start = new Date(date.slice(0, 10) + "T00:00:00Z");
  const end = new Date(start.getTime() + 86_400_000);
  return { start_at: start.toISOString(), end_at: end.toISOString() };
}

async function collectDay(page: Page, date: string) {
  await tab(page, "消费归属").click();
  await page.getByLabel("消费账号").selectOption({ label: account });
  const range = dates(date);
  for (const [label, value] of [["消费起始时间（含）", range.start_at], ["消费结束时间（不含）", range.end_at]]) {
    await button(page, label + "：高级时间设置").click();
    await page.getByRole("textbox", { name: label }).fill(value);
  }
  await button(page, "查询消费").click();
  await button(page, "立即采集").click();
  return range;
}

async function download(page: Page, trigger: () => Promise<void>, filename: string) {
  const [file] = await Promise.all([page.waitForEvent("download", { timeout: 30_000 }), trigger()]);
  expect(file.suggestedFilename()).toMatch(/\.spbak$/);
  await file.saveAs(join(app.work, filename));
}

test.beforeEach(async () => { app = await startServices(); });
test.afterEach(async ({}, info) => {
  if (!app) return;
  try {
    if (info.status !== info.expectedStatus) {
      await info.attach("service diagnostics", { body: app.logs(), contentType: "text/plain" });
    }
  } finally {
    await app.stop();
  }
});

function watchErrors(target: Page, errors: string[], restoring = () => false) {
  target.on("console", message => {
    if (message.type() !== "error") return;
    const url = message.location().url;
    const text = message.text();
    const pathname = new URL(target.url()).pathname;
    if (url === app.origin + "/api/v1/auth/session" && text.includes("status of 401 (Unauthorized)") &&
        (pathname === "/login" || pathname === "/change-password" || restoring())) return;
    errors.push(url + " " + text);
  });
  target.on("pageerror", error => errors.push(target.url() + " " + error.message));
}

test("setup, usage, attribution, wallets, access and console navigation", async ({ page, browser }) => {
  const errors: string[] = [];
  watchErrors(page, errors);
  const readerContext = await browser.newContext({ timezoneId: "UTC" });
  const reader = await readerContext.newPage();
  watchErrors(reader, errors);
  const open = (path: string) => page.goto(app.origin + path);

  try {
    await test.step("AC1 first login and password change", async () => {
      await firstLogin(page);
      await expect(page.getByText("保存统计时区", { exact: true })).toBeVisible();
    });

    await test.step("AC2 connection, account adoption and scheduled observation", async () => {
      await open("/console/connection");
      await page.getByLabel(/服务地址/).fill(app.fixtureOrigin);
      await page.getByPlaceholder("输入管理凭据").fill(fixtureKey);
      await button(page, "保存").click();
      await expect(page.getByText("已配置（原文不可读取）", { exact: true })).toBeVisible();
      await button(page, "重新读取").click();
      const [tested] = await Promise.all([
        page.waitForResponse(response => response.url().endsWith("/api/v1/connection/test") && response.request().method() === "POST"),
        button(page, "测试连接").click(),
      ]);
      expect(tested.status()).toBe(200);
      expect(await tested.json()).toMatchObject({ version: "0.2.23", saved: true });
      await expect(page.getByRole("listitem").filter({ hasText: /部署版本.*已验证此项读取/ })).toBeVisible();
      await test.step("F-9 capability table discloses technical evidence only on demand", async () => {
        await tab(page, "能力检查").click();
        const table = page.getByRole("table");
        await expect(table.getByRole("columnheader")).toHaveText(["能力", "状态", "说明"]);
        for (const id of ["health", "version.read", "run_mode"]) {
          await expect(table.getByText(id, { exact: true })).toBeHidden();
        }
        const service = table.getByRole("row").filter({ hasText: "服务可达" });
        await expect(service.getByRole("cell", { name: "已验证此项读取", exact: true })).toBeVisible();
        const details = service.locator("summary");
        const id = service.getByText("health", { exact: true });
        const http = service.getByText(/HTTP 200/);
        await expect(http).toBeHidden();
        await expect(details).toHaveText("技术详情");
        await details.press("Enter");
        await expect(id).toBeVisible();
        await expect(http).toBeVisible();
        await details.press("Enter");
        await expect(id).toBeHidden();

        const viewport = page.viewportSize();
        await page.setViewportSize({ width: 390, height: 844 });
        await details.click();
        await expect(id).toBeVisible();
        expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
        await details.click();
        await expect(id).toBeHidden();
        if (viewport) await page.setViewportSize(viewport);
      });
      await setSchedule(page, false);
      await tab(page, "账号纳管").click();
      await button(page, "纳管").click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("textbox").fill(account);
      await dialog.getByRole("button", { name: /保存|确认|纳管/ }).click();
      await expect(dialog).toBeHidden();
      await expect(page.getByRole("switch", { name: account + "的本地监控" })).toHaveAttribute("aria-checked", "true");
      await setSchedule(page, true);
      await expect(async () => {
        await open("/console/observations");
        await expect(row(page, account).getByRole("cell", { name: "25%", exact: true }).first()).toBeVisible({ timeout: 2500 });
      }).toPass({ timeout: 30_000 });
    });

    await test.step("F-16 navigation follows current source grants and offers opening conditions", async () => {
      const nav = page.getByRole("navigation", { name: "主导航", exact: true });
      const features = [
        { label: "分组计价", grant: "计价" },
        { label: "账号暂停", grant: "整账号调度" },
      ];
      const featureLink = (label: string) => nav.getByRole("link", { name: label, exact: true });
      await open("/console");
      for (const feature of features) {
        const link = featureLink(feature.label);
        await expect(link).toHaveAccessibleDescription("未开放");
        await link.click();
        await expect(page.getByRole("heading", { name: "开放条件", exact: true })).toBeVisible();
        await expect(page.getByText("由管理员在「连接与采集 → 能力检查 → 生产写入授权」开启" + feature.grant + "授权。", { exact: true })).toBeVisible();
      }
      await page.getByRole("link", { name: "前往连接与采集", exact: true }).click();
      await expect(button(page, "测试连接")).toBeEnabled();
      await tab(page, "能力检查").click();
      for (const feature of features) {
        const link = featureLink(feature.label);
        await button(page, "开启" + feature.grant + "授权").click();
        const dialog = page.getByRole("dialog");
        await dialog.getByLabel("当前管理员密码", { exact: true }).fill(app.adminPassword);
        await dialog.getByRole("button", { name: "确认开启", exact: true }).click();
        await expect(dialog).toBeHidden();
        await expect(link).toHaveText(feature.label);
        const other = features.find(item => item !== feature);
        if (other) await expect(featureLink(other.label)).toHaveAccessibleDescription("未开放");
        await button(page, "撤销" + feature.grant + "授权").click();
        await dialog.getByLabel("当前管理员密码", { exact: true }).fill(app.adminPassword);
        await dialog.getByRole("button", { name: "确认撤销", exact: true }).click();
        await expect(dialog).toBeHidden();
        await expect(link).toHaveAccessibleDescription("未开放");
      }
      const viewport = page.viewportSize();
      await page.setViewportSize({ width: 390, height: 844 });
      await button(page, "打开菜单").click();
      for (const feature of features) await expect(featureLink(feature.label)).toHaveAccessibleDescription("未开放");
      await featureLink("账号暂停").click();
      await expect(page.getByRole("heading", { name: "开放条件", exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
      if (viewport) await page.setViewportSize(viewport);
    });

    await test.step("F-E1 an uncertain grant reply keeps the warning and rereads without replay", async () => {
      await open("/console/connection");
      await expect(button(page, "测试连接")).toBeEnabled();
      await tab(page, "能力检查").click();
      const updatePath = "**/api/v1/connection/write-grants/update";
      let writes = 0, receivedStatus = 0;
      await page.route(updatePath, async route => {
        writes++;
        const response = await route.fetch();
        receivedStatus = response.status();
        await route.fulfill({ response, json: {} });
      });
      const dialog = page.getByRole("dialog");
      try {
        await button(page, "开启计价授权").click();
        await dialog.getByLabel("当前管理员密码", { exact: true }).fill(app.adminPassword);
        await dialog.getByRole("button", { name: "确认开启", exact: true }).click();
        await expect(dialog).toBeHidden();
        await expect(page.getByText(/操作结果尚未确认/)).toBeVisible();
        await expect(button(page, "撤销计价授权")).toBeEnabled();
        expect(receivedStatus).toBe(200);
        expect(writes).toBe(1);
      } finally {
        await page.unroute(updatePath);
      }
      await button(page, "撤销计价授权").click();
      await dialog.getByLabel("当前管理员密码", { exact: true }).fill(app.adminPassword);
      await dialog.getByRole("button", { name: "确认撤销", exact: true }).click();
      await expect(dialog).toBeHidden();
      await expect(button(page, "开启计价授权")).toBeEnabled();
    });

    await test.step("AC3 collect a closed day and display the two usage entries", async () => {
      const history = await fixtureRead<{ data: { items: { id: number; created_at: string }[] } }>("usage?account_id=1");
      expect(history.data.items.map(item => item.id).sort()).toEqual([1, 2]);
      await open("/console/pools");
      await collectDay(page, history.data.items[0].created_at);
      await expect(page.getByRole("listitem").filter({ hasText: /采集完成 · 2 \/ 2 条/ })).toBeVisible();
      await open("/console/usage");
      await tab(page, "费用明细").click();
      for (const id of [1, 2]) {
        const entry = page.getByRole("row").filter({ has: page.locator("time[title*='原生行 " + id + "；']") });
        await expect(entry.getByRole("cell", { name: /^0\.10?$/ })).toBeVisible();
        await expect(entry.getByRole("cell", { name: /^0\.20?$/ })).toBeVisible();
      }
    });

    await test.step("AC4 bind a participant, record pool shares and attribute new usage", async () => {
      await open("/console/pools");
      await tab(page, "参与者").click();
      await memberForm(page, member, "fixture@example.invalid");
      await memberForm(page, otherMember, "second@example.invalid");
      await tab(page, "额度池与份额").click();
      await button(page, "新建额度池").click();
      const dialog = page.getByRole("dialog");
      await dialog.getByLabel("池名称", { exact: true }).fill(pool);
      await dialog.getByRole("checkbox", { name: /^Browser account · 1$/ }).check();
      await dialog.getByRole("spinbutton", { name: member + " 份额" }).fill("100");
      const [saved] = await Promise.all([
        page.waitForResponse(response => response.url().endsWith("/api/v1/pools/create") && response.request().method() === "POST"),
        button(page, "创建额度池").click(),
      ]);
      expect(saved.status()).toBe(200);
      const contract = await saved.json() as { effective_at: string; participants: { name: string; share_percent: string }[] };
      expect(contract.participants).toEqual(expect.arrayContaining([expect.objectContaining({ name: member, share_percent: "100" })]));
      await expect(dialog).toBeHidden();
      await tab(page, "分配依据历史").click();
      await expect(row(page, pool)).toContainText(member + " 100%");
      const at = await app.recordUsage();
      expect(Date.parse(at)).toBeGreaterThanOrEqual(Date.parse(contract.effective_at));
      const range = await collectDay(page, at);
      await expect(row(page, member).getByRole("cell", { name: /^0\.10?$/ }).first()).toBeVisible({ timeout: 30_000 });

      const accountId = await page.getByLabel("消费账号").inputValue();
      const recorded = await json<{ view: { records: { fact: { id: string; created_at: string }; attribution: { participant_name: string; basis: string } }[] } | null }>(
        await page.request.post(app.origin + "/api/v1/participants/usage/recorded", {
          headers: { origin: app.origin },
          data: { ...range, account_ids: [accountId], participant_id: "", after: "0", limit: 100, grain: "day", model: "", api_key_id: "", basis: "" },
        }),
      );
      const fact = recorded.view?.records.find(item => item.fact.id === "3");
      expect(fact?.attribution).toMatchObject({ participant_name: member, basis: "local_binding_at_log_time" });
      expect(Date.parse(fact!.fact.created_at)).toBeGreaterThanOrEqual(Date.parse(contract.effective_at));
    });

    await test.step("AC5 read balance, authorize wallet writes and confirm one adjustment", async () => {
      await open("/console/balances");
      const wallet = row(page, member);
      const status = page.getByRole("status", { name: "余额调整状态" });
      await expect(status).toContainText("本页余额调整 · 暂不可发起");
      await expect(page.getByText(/本页钱包尚未取得读数.*余额与冻结额读数.*调整记录/)).toBeVisible();
      await expect(page.getByRole("main").getByRole("checkbox")).toHaveCount(0);
      await wallet.getByText(member, { exact: true }).click();
      await button(page, "刷新钱包读数").click();
      await expect(wallet.getByRole("cell", { name: /^100(?:\.00?)?$/ })).toBeVisible();

      await open("/console/connection");
      await expect(button(page, "测试连接")).toBeEnabled();
      await tab(page, "能力检查").click();
      await button(page, "开启钱包授权").click();
      await page.getByLabel("当前管理员密码", { exact: true }).fill(app.adminPassword);
      await button(page, "确认开启").click();
      await expect(button(page, "撤销钱包授权")).toBeVisible();

      await open("/console/balances");
      await expect(status).toContainText("本页余额调整 · 可发起");
      await expect(status).toContainText("派发前会重新核对来源授权和钱包状态");
      await expect(page.getByText("能力门禁：余额调整", { exact: true })).toHaveCount(0);
      await wallet.getByTitle("对当前 Sub2API 用户提交原生余额调整").click();
      const dialog = page.getByRole("dialog");
      await dialog.getByLabel(/^余额增减额/).fill("1.25");
      await dialog.getByRole("checkbox", { name: "确认调整用户和余额增减额。" }).check();
      await button(page, "确认调整").click();
      await expect(dialog.getByText(/原生余额调整已核对流水 #1和独立余额读数/)).toBeVisible();
      await expect(dialog.getByText("101.25", { exact: true })).toBeVisible();
      const ledger = await fixtureRead<{ data: { total: number; items: { value: number }[] } }>("users/21/balance-history?page=1&page_size=10");
      expect(ledger.data.total).toBe(1);
      expect(ledger.data.items[0].value).toBe(1.25);
    });

    await test.step("weekly recommendation calculates first and allows explicit manual application", async () => {
      await open("/console/balances");
      const wallet = row(page, member), dialog = page.getByRole("dialog");
      const path = /\/api\/v1\/pools\/(?:get|weekly-wallet(?:\/(?:apply|configure|recommendation))?)$/;
      const poolID = Buffer.alloc(16, 1).toString("base64url"), signed = "a".repeat(64);
      const period = new Date(Date.now() + 3600000).toISOString();
      const plan = { gross: "125", spent: "5", opening_debt: "0", debt: "0", balance_before: "101.25",
        target: "120", contract_id: "1", estimate_id: "1", share_percent: "100" };
      const requests: string[] = [], applications: unknown[] = [];
      let waiting = true, basis: string | null = null, memberID = "";
      await page.route(path, async route => {
        const action = new URL(route.request().url()).pathname.split("/").at(-1);
        if (!action) throw new Error("Missing weekly wallet action");
        if (action === "get") {
          await route.fulfill({ json: {
            id: "1", pool_id: poolID, revision: "1", name: pool, enabled: true, source_id: poolID, connection_revision: "1",
            effective_at: period, ended_at: null, current_revision: "1", is_current: true, source_current: true, visibility: "all_members",
            accounts: [{ id: Buffer.alloc(16, 2).toString("base64url"), revision: "1", name: account, source_id: poolID, upstream_id: "1", identity_current: true }],
            participants: [], unallocated_percent: "100",
          } });
          return;
        }
        requests.push(action);
        if (action === "recommendation") {
          memberID = route.request().postDataJSON().id;
          const state = waiting ? "waiting_estimate" : applications.length ? "settled" : "ready";
          const reference = waiting ? { amount: "80", accounts: [{
            pool_id: poolID, account_id: Buffer.alloc(16, 2).toString("base64url"), name: account, share_percent: "40",
            amount: "80", basis_at: "2026-11-25T03:36:55Z", used_percent: "56", samples: 2, reason: "collecting_samples",
          }] } : undefined;
          await route.fulfill({ json: { participant_id: memberID, pool: { id: poolID, name: pool }, state,
            reference, proposal: state === "ready" ? { basis, period_end: period, plan, delta: "18.75" } : null } });
        } else if (action === "apply") {
          applications.push(route.request().postDataJSON());
          await route.fulfill({ json: { id: "1", participant_id: memberID, name: member, phase: "settlement",
            period_end: period, recorded_at: new Date().toISOString(), application_id: "1", state: "applied", plan } });
        } else if (action === "weekly-wallet") {
          await route.fulfill({ json: { pool_id: poolID, pool_revision: "1", revision: "1", enabled: false, auto_allocate: false,
            state: "disabled", entries: [] } });
        } else {
          await route.abort();
        }
      });
      try {
        await wallet.getByRole("button", { name: "周额度建议", exact: true }).click();
        await expect(dialog.getByRole("columnheader", { name: "参考周额度" })).toBeVisible();
        await expect(dialog.getByRole("cell", { name: "80", exact: true })).toBeVisible();
        await expect(dialog.getByRole("cell", { name: "2/3", exact: true })).toBeVisible();
        await expect(dialog.locator('time[datetime="2026-11-25T03:36:55Z"]')).toBeVisible();
        await expect(dialog.getByText("等待同周期新增样本")).toBeVisible();
        await expect(button(page, "应用建议余额")).toHaveCount(0);
        waiting = false;
        await button(page, "重新计算").click();
        await expect(dialog.getByText(/建议余额为 120/)).toBeVisible();
        await expect(dialog.getByRole("columnheader", { name: "参考周额度" })).toHaveCount(0);
        await expect(button(page, "应用建议余额")).toHaveCount(0);
        await expect(button(page, "保存设置")).toHaveCount(0);
        expect(requests).toEqual(["recommendation", "recommendation"]);
        await dialog.getByRole("button", { name: "关闭", exact: true }).click();

        basis = signed;
        await wallet.getByText(member, { exact: true }).click();
        await dialog.getByRole("button", { name: "周额度建议", exact: true }).click();
        const apply = button(page, "应用建议余额");
        await expect(apply).toBeDisabled();
        await dialog.getByRole("checkbox", { name: /确认这是该参与者的专用钱包/ }).check();
        await apply.click();
        await expect(dialog.getByText(/正式结算已确认/)).toBeVisible();
        expect(applications).toEqual([{ pool_id: poolID, participant_id: memberID, basis: signed }]);
        expect(requests).not.toContain("weekly-wallet");
        await dialog.getByRole("button", { name: "关闭", exact: true }).click();

        await tab(page, "自动调整").click();
        await wallet.getByRole("button", { name: "周额度设置", exact: true }).click();
        await expect(dialog.getByRole("checkbox", { name: "开启半额预发", exact: true })).not.toBeChecked();
        await expect(button(page, "保存设置")).toBeEnabled();
        await expect(apply).toHaveCount(0);
        expect(requests.at(-1)).toBe("weekly-wallet");
        expect(requests).not.toContain("configure");
        await dialog.getByRole("button", { name: "关闭", exact: true }).click();
      } finally {
        await page.unroute(path);
      }
    });

    await test.step("AC6 grant a subset and verify ordinary-user data and route access", async () => {
      await open("/console/users");
      const dialog = await userForm(page, ordinary);
      await tab(page, "功能权限").click();
      await dialog.getByRole("checkbox", { name: "账号与额度", exact: true }).check();
      await dialog.getByRole("checkbox", { name: "参与者与额度池", exact: true }).check();
      await dialog.getByRole("checkbox", { name: "余额建议与调整", exact: true }).check();
      await dialog.getByRole("checkbox", { name: "分组计价", exact: true }).check();
      await dialog.getByRole("checkbox", { name: "通知", exact: true }).check();
      await tab(page, "数据范围").click();
      await dialog.getByRole("checkbox", { name: account, exact: true }).check();
      await dialog.getByRole("checkbox", { name: member, exact: true }).check();
      await dialog.getByRole("button", { name: "保存", exact: true }).click();
      await expect(dialog).toBeHidden();
      await expect(row(page, ordinary)).toBeVisible();

      const administratorReads: string[] = [];
      reader.on("request", request => {
        const path = new URL(request.url()).pathname;
        if (path === "/api/v1/connection" || path === "/api/v1/connection/write-grants") administratorReads.push(path);
      });
      await login(reader, ordinary, app.userPassword);
      await expect(reader).toHaveURL(/\/console$/);
      const nav = reader.getByRole("navigation", { name: "主导航" });
      await nav.getByRole("link", { name: "参与者与额度池", exact: true }).click();
      await tab(reader, "参与者").click();
      await expect(row(reader, member)).toBeVisible();
      await expect(row(reader, otherMember)).toHaveCount(0);
      await expect(nav.getByRole("link", { name: "连接与采集" })).toHaveCount(0);
      const pricingLink = nav.getByRole("link", { name: "分组计价", exact: true });
      await expect(pricingLink).toHaveAccessibleDescription("未开放");
      await pricingLink.click();
      await expect(reader.getByRole("heading", { name: "开放条件", exact: true })).toBeVisible();
      expect(administratorReads).toEqual([]);
      await reader.goto(app.origin + "/console/connection");
      await expect(reader.getByText(/无权限|无权访问|未授予/).first()).toBeVisible();
      expect((await reader.request.get(app.origin + "/api/v1/connection")).status()).toBe(403);
    });

    await test.step("F-8 wallet details refresh after authorization changes", async () => {
      const drawer = reader.getByRole("dialog");
      const openWallet = () => row(reader, member).getByText(member, { exact: true }).click();
      await reader.goto(app.origin + "/console/balances");
      await openWallet();
      await expect(drawer.getByText(/展示最近完整确认读数/)).toBeVisible();
      await expect(drawer.getByText(account, { exact: true })).toHaveCount(0);
      await expect(reader.getByRole("status", { name: "余额调整状态" })).toHaveCount(0);

      await row(page, ordinary).getByRole("button", { name: "编辑", exact: true }).click();
      const editor = page.getByRole("dialog");
      await tab(page, "功能权限").click();
      await editor.getByRole("checkbox", { name: "账号与额度", exact: true }).uncheck();
      await editor.getByRole("button", { name: "保存", exact: true }).click();
      await expect(editor).toBeHidden();
      await reader.evaluate(() => window.dispatchEvent(new StorageEvent("storage", { key: "sp.auth.invalidate", newValue: "refresh:f8" })));
      await expect(drawer).toBeHidden();
      await openWallet();
      await expect(drawer.getByText(/展示最近完整确认读数/)).toBeVisible();
      await expect(drawer.getByText(account, { exact: true })).toHaveCount(0);
    });

    await test.step("F-6 filter notifications by authorized names and an exact time range", async () => {
      const accountReads: string[] = [];
      reader.on("request", request => {
        if (new URL(request.url()).pathname === "/api/v1/accounts/list") accountReads.push(request.method());
      });
      await reader.goto(app.origin + "/console/notifications");
      const readerMembers = reader.getByLabel("通知参与者", { exact: true });
      await expect(readerMembers.getByRole("option", { name: member, exact: true })).toHaveCount(1);
      await expect(readerMembers.getByRole("option", { name: otherMember, exact: true })).toHaveCount(0);
      await reader.getByLabel("通知相关对象类型", { exact: true }).selectOption("account");
      await expect(reader.getByLabel("通知相关对象", { exact: true })).toBeDisabled();
      expect(accountReads).toEqual([]);

      await page.setViewportSize({ width: 1280, height: 860 });
      await open("/console/notifications");
      await tab(page, "通知记录").click();
      const kindOptions = page.getByLabel("通知类型", { exact: true }).locator("option");
      await expect(kindOptions).toHaveText(["全部", "测试邮件", "余额低", "钱包建议变化", "估算显著变化", "采集失败", "临时轮次接近上限", "临时轮次已耗尽"]);
      const start = page.getByLabel("通知开始时间（含）", { exact: true });
      const end = page.getByLabel("通知结束时间（不含）", { exact: true });
      await expect(start).toHaveAttribute("type", "datetime-local");
      await expect(end).toHaveAttribute("type", "datetime-local");
      const tops = await Promise.all([page.getByLabel("通知类型", { exact: true }), start, end]
        .map(field => field.evaluate(element => element.getBoundingClientRect().top)));
      expect(Math.max(...tops) - Math.min(...tops)).toBeLessThanOrEqual(1);

      const [participantID] = await page.getByLabel("通知参与者", { exact: true }).selectOption({ label: member });
      await page.getByLabel("通知相关对象类型", { exact: true }).selectOption("account");
      const [accountID] = await page.getByLabel("通知相关对象", { exact: true }).selectOption({ label: account });
      await button(page, "通知开始时间（含）：高级时间设置").click();
      await button(page, "通知结束时间（不含）：高级时间设置").click();
      await expect(start).toHaveAccessibleDescription("精确 UTC · 例 2026-11-20T14:23:00Z");
      const startAt = "2026-11-23T00:00:00.123456789Z", endAt = "2026-11-24T00:00:00.987654321Z";
      await start.fill(startAt);
      const matchingRead = page.waitForResponse(response => {
        const request = response.request();
        if (!response.url().endsWith("/api/v1/notifications/list") || request.method() !== "POST") return false;
        const filter = request.postDataJSON();
        return filter?.participant_id === participantID && filter.subject_type === "account" && filter.subject_id === accountID
          && filter.start_at === startAt && filter.end_at === endAt;
      });
      await end.fill(endAt);
      expect((await matchingRead).status()).toBe(200);
      await button(page, "通知开始时间（含）：使用日期选择").click();
      await expect(start).toHaveAttribute("type", "datetime-local");
      await button(page, "通知开始时间（含）：高级时间设置").click();
      await expect(start).toHaveValue(startAt);
      await page.setViewportSize({ width: 390, height: 860 });
      await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    });

    await test.step("F-E1 cancels a pool editor read when leaving the page", async () => {
      await page.setViewportSize({ width: 1280, height: 860 });
      await open("/console/pools");
      await tab(page, "参与者").click();
      const path = "**/api/v1/participants/get";
      let received = 0;
      let releaseRead: () => void = () => {};
      const delivery = new Promise<void>(resolve => { releaseRead = resolve; });
      await page.route(path, async route => {
        const response = await route.fetch();
        received = response.status();
        await delivery;
        await route.fulfill({ response });
      }, { times: 1 });
      try {
        await row(page, otherMember).getByRole("button", { name: "编辑", exact: true }).click();
        await expect.poll(() => received).toBe(200);
        await expect(page.getByText("正在读取当前编辑依据…", { exact: true })).toBeVisible();
        const cancelled = page.waitForEvent("requestfailed", {
          predicate: request => request.url().endsWith("/api/v1/participants/get"),
          timeout: 10_000,
        });
        await page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "账号与额度", exact: true }).click();
        await expect(page).toHaveURL(/\/console\/accounts$/);
        expect((await cancelled).failure()?.errorText).toContain("ERR_ABORTED");
        releaseRead();
        await expect(page.getByRole("dialog")).toHaveCount(0);
      } finally {
        releaseRead();
        await page.unroute(path);
      }
    });

    await test.step("F-E1 an unverified participant exit is reconciled without another write", async () => {
      await open("/console/pools");
      await tab(page, "参与者").click();
      const dialog = page.getByRole("dialog");
      const path = /\/api\/v1\/participants\/exit(?:-result)?$/;
      let writes = 0, reads = 0, writeStatus = 0;
      await page.route(path, async route => {
        const response = await route.fetch();
        if (route.request().url().endsWith("/exit-result")) {
          reads++;
          if (reads === 1) {
            await route.fulfill({ response, json: {} });
            return;
          }
        } else {
          writes++;
          writeStatus = response.status();
        }
        await route.fulfill({ response });
      });
      try {
        await row(page, otherMember).getByRole("button", { name: "退出", exact: true }).click();
        await dialog.getByRole("button", { name: "确认退出", exact: true }).click();
        await expect(dialog.getByText(/退出结果尚未完成核对/)).toBeVisible();
        await dialog.getByRole("button", { name: "取消", exact: true }).click();
        await expect(dialog).toBeHidden();
        await expect(button(page, "新建参与者")).toBeDisabled();
        await button(page, "重新读取").click();
        await dialog.getByRole("button", { name: "核对退出结果", exact: true }).click();
        await expect(dialog).toBeHidden();
        await expect(row(page, otherMember)).toContainText("已停用");
        await expect(button(page, "新建参与者")).toBeEnabled();
        expect({ writeStatus, writes, reads }).toEqual({ writeStatus: 200, writes: 1, reads: 2 });
      } finally {
        await page.unroute(path);
      }
    });

    await test.step("F-E1 an old participant catalog cannot restore a revoked view", async () => {
      await reader.goto(app.origin + "/console/pools");
      await tab(reader, "参与者").click();
      await expect(row(reader, member)).toBeVisible();

      const path = "**/api/v1/participants/list";
      let received = 0;
      let releaseRead: () => void = () => {};
      const delivery = new Promise<void>(resolve => { releaseRead = resolve; });
      await reader.route(path, async route => {
        const response = await route.fetch();
        received = response.status();
        await delivery;
        await route.fulfill({ response });
      }, { times: 1 });
      try {
        await reader.evaluate(() => window.dispatchEvent(new Event("focus")));
        await expect.poll(() => received).toBe(200);
        await open("/console/users");
        await row(page, ordinary).getByRole("button", { name: "编辑", exact: true }).click();
        const editor = page.getByRole("dialog");
        await tab(page, "功能权限").click();
        await editor.getByRole("checkbox", { name: "参与者与额度池", exact: true }).uncheck();
        await editor.getByRole("button", { name: "保存", exact: true }).click();
        await expect(editor).toBeHidden();
        await reader.evaluate(() => window.dispatchEvent(new StorageEvent("storage", { key: "sp.auth.invalidate", newValue: "refresh:fe1-pools" })));
        await expect(reader.getByText(/无权限|无权访问|未授予/).first()).toBeVisible();
        releaseRead();
        await expect(reader.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "参与者与额度池", exact: true })).toHaveCount(0);
        await expect(row(reader, member)).toHaveCount(0);
      } finally {
        releaseRead();
        await reader.unroute(path);
      }
    });

    await test.step("AC8 console links, 390px and browser errors", async () => {
      const links = [
        ["", "概览"], ["/accounts", "账号与额度"], ["/usage", "用量与费用"], ["/observations", "观测记录"],
        ["/estimates", "容量估算"], ["/pools", "参与者与额度池"], ["/balances", "余额建议与调整"],
        ["/pricing", "分组计价"], ["/dispatch", "账号暂停"], ["/connection", "连接与采集"],
        ["/notifications", "通知"], ["/users", "用户与权限"], ["/security", "登录与审计"],
        ["/api-keys", "API Key"], ["/maintenance", "历史维护"], ["/backup", "备份与恢复"], ["/help", "帮助与公告"],
      ];
      const nav = page.getByRole("navigation", { name: "主导航" });
      const main = page.getByRole("main");
      const heading = main.getByRole("heading", { level: 1 });
      const temporaryLink = main.locator('a[href="/console/temporary"]');
      const assertPage = async (path: string, width: number, previousHeading: string) => {
        await expect(page).toHaveURL(app.origin + "/console" + path);
        await expect(heading).toBeVisible();
        await expect(heading).not.toHaveText(previousHeading);
        await expect(main).not.toContainText("S2A-");
        expect(await main.innerText()).not.toMatch(/\b(?:actual_cost|frozen_balance|api-keys-v1|CAS|algorithm_version|epoch|revision)\b|算法版本/);
        const intro = heading.locator("..").locator(":scope > p");
        if (await intro.count()) {
          expect(await intro.evaluate(element => {
            const range = document.createRange();
            range.selectNodeContents(element);
            return new Set(Array.from(range.getClientRects(), rect => Math.round(rect.top))).size;
          }), path + " header description").toBeLessThanOrEqual(1);
        }
        if (width === 390) {
          await expect.poll(() => page.evaluate(() => Math.max(document.documentElement.scrollWidth, document.body.scrollWidth)), {
            message: "Document width at " + path,
          }).toBeLessThanOrEqual(width);
        }
      };
      for (const width of [1280, 390]) {
        await page.setViewportSize({ width, height: 860 });
        for (const [path, label] of links) {
          const previous = await heading.innerText();
          if (width === 390) await button(page, "打开菜单").click();
          if (path === "") await expect(nav.getByRole("link", { name: "临时放宽", exact: true })).toHaveCount(0);
          await nav.getByRole("link", { name: label, exact: true }).click();
          await assertPage(path, width, previous);
          if (path === "") await expect(temporaryLink).toHaveCount(0);
        }
        const previous = await heading.innerText();
        await page.getByRole("banner").getByRole("button", { name: /^A(?: admin)?$/ }).click();
        await page.getByRole("link", { name: "个人设置与密码", exact: true }).click();
        await assertPage("/profile", width, previous);

        const profileHeading = await heading.innerText();
        await open("/console/temporary");
        await assertPage("/temporary", width, profileHeading);
        await expect(main.getByText("当前来源没有轮次记录。", { exact: true })).toBeVisible();
        await expect(button(page, "启用前核验")).toHaveCount(0);
        await expect(tab(page, "当前轮次")).toHaveCount(0);
        await main.getByRole("link", { name: "了解更多", exact: true }).click();
        await expect(page.getByRole("button", { name: "临时放宽历史" })).toBeVisible();
      }
      expect(errors.map(app.redact)).toEqual([]);
    });

    await test.step("F-12 help avoids demo requests from the legacy tab URL", async () => {
      const demoRequests: string[] = [];
      page.on("request", request => {
        if (new URL(request.url()).pathname === "/api/v1/demo/scenario") demoRequests.push(request.method());
      });
      await open("/console/help?tab=demo");
      await expect(page.getByRole("tab", { name: "使用说明", selected: true })).toBeVisible();
      await expect(tab(page, "演示模式")).toHaveCount(0);
      await expect(page.getByRole("button", { name: "临时放宽历史" })).toBeVisible();
      expect(demoRequests).toEqual([]);
    });

    await test.step("临时放宽历史使用真实只读接口，旧操作拒绝", async () => {
      await page.setViewportSize({ width: 1280, height: 860 });
      const paths: string[] = [];
      page.on("request", request => {
        const path = new URL(request.url()).pathname;
        if (path.startsWith("/api/v1/temporary/")) paths.push(path);
      });
      await open("/console");
      const historyLink = page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "临时放宽历史", exact: true });
      await expect(historyLink).toBeVisible();
      await test.step("F-4 participant balances fit their content on desktop and mobile", async () => {
        const balanceCard = page.locator("section").filter({ has: page.getByRole("heading", { name: "参与者余额", exact: true }) });
        await expect(balanceCard.getByText(member, { exact: true })).toBeVisible();
        const measure = () => balanceCard.evaluate(card => {
          const body = card.lastElementChild;
          const neighbor = card.nextElementSibling;
          if (!body || !neighbor) throw new Error("Missing overview card layout");
          const bounds = card.getBoundingClientRect();
          const side = neighbor.getBoundingClientRect();
          return {
            top: bounds.top, bottom: bounds.bottom,
            gap: Math.abs(bounds.bottom - body.getBoundingClientRect().bottom),
            neighborTop: side.top, neighborBottom: side.bottom,
          };
        });
        const desktop = await measure();
        expect(desktop.neighborTop).toBeCloseTo(desktop.top);
        expect(desktop.gap).toBeLessThanOrEqual(2);
        expect(desktop.neighborBottom).toBeGreaterThan(desktop.bottom);
        await page.setViewportSize({ width: 390, height: 860 });
        const mobile = await measure();
        expect(mobile.neighborTop).toBeGreaterThan(mobile.bottom);
        expect(mobile.gap).toBeLessThanOrEqual(2);
        await page.setViewportSize({ width: 1280, height: 860 });
      });
      await historyLink.click();
      await expect(page.getByRole("heading", { level: 1, name: "临时放宽历史" })).toBeVisible();
      await expect(page.getByText("当前来源没有轮次记录。", { exact: true })).toBeVisible();
      expect(paths).toEqual(["/api/v1/temporary/history"]);
      const response = await page.request.post(app.origin + "/api/v1/temporary/start", { data: {}, headers: { Origin: app.origin } });
      expect(response.status()).toBe(404);
    });

    await test.step("3.2 help links open the relevant guide", async () => {
      await open("/console/usage");
      await page.getByRole("link", { name: "了解更多", exact: true }).click();
      await expect(page).toHaveURL(/\/console\/help\?guide=usage$/);
      await expect(page.getByRole("heading", { name: "用量采集与费用口径", exact: true })).toBeVisible();
    });

    await test.step("F-E3 connection displays a correlated deployment hint", async () => {
      const id = "e".repeat(32);
      await page.route("**/api/v1/connection", route => route.fulfill({
        status: 502, headers: { "X-Request-ID": id },
        json: {
          defined: false, code: "CONTRACT_MISMATCH", status: 502,
          message: `前后端版本不一致，请检查部署。 请求编号 ${id}`,
          data: { route: "/api/v1/connection", expected_version: "connection-v2", request_id: id },
        },
      }));
      await open("/console/connection");
      await expect(page.getByText(/前后端版本不一致，请检查部署。/)).toContainText(`请求编号 ${id}`);
      await page.unroute("**/api/v1/connection");
    });

    await test.step("F-7 filter in the saved statistics timezone using the visible example", async () => {
      const settingsURL = app.origin + "/api/v1/settings";
      const { revision } = await json<{ revision: string }>(await page.request.get(settingsURL));
      const timezone = "America/New_York";
      const saved = await json<{ timezone: string }>(await page.request.post(settingsURL + "/update", {
        headers: { origin: app.origin }, data: { revision, timezone },
      }));
      expect(saved.timezone).toBe(timezone);
      await reader.goto(app.origin + "/console/notifications");
      const start = reader.getByLabel("通知开始时间（含）", { exact: true });
      const end = reader.getByLabel("通知结束时间（不含）", { exact: true });
      const example = `统计时区 ${timezone} · 例 2026-11-20 14:23:00`;
      await expect(reader.getByText(example, { exact: true }).first()).toBeVisible();
      await expect(start).toHaveAccessibleDescription(example);
      await expect(button(reader, "通知开始时间（含）：高级时间设置")).toHaveText("高级");
      for (const wallTime of ["2026-03-08T02:30", "2026-11-01T01:30"]) {
        await start.fill(wallTime);
        await expect(start).toHaveAttribute("aria-invalid", "true");
      }
      await start.fill("2026-11-24T14:23:01");
      const filtered = reader.waitForResponse(response => {
        const request = response.request();
        if (!response.url().endsWith("/api/v1/notifications/list") || request.method() !== "POST") return false;
        const filter = request.postDataJSON();
        return filter?.start_at === "2026-11-24T19:23:01.000Z" && filter.end_at === "2026-11-24T19:24:02.000Z";
      });
      await end.fill("2026-11-24T14:24:02");
      expect((await filtered).status()).toBe(200);
    });

    await test.step("F-5 overview formats usage times and wraps long subtitles in both mobile columns", async () => {
      await page.setViewportSize({ width: 390, height: 860 });
      const [response] = await Promise.all([
        page.waitForResponse(value => value.url() === app.origin + "/api/v1/participants/usage/recorded" && value.request().method() === "POST"),
        open("/console"),
      ]);
      expect(response.status()).toBe(200);
      const { view } = await response.json() as { view: { start_at: string; end_at: string; history: { timezone: string } } | null };
      if (!view) throw new Error("The overview fixture has no recorded usage interval");
      expect(view.history.timezone).toBe("America/New_York");
      const format = new Intl.DateTimeFormat("zh-CN", {
        timeZone: view.history.timezone, dateStyle: "short", timeStyle: "medium", hourCycle: "h23",
      });
      const card = page.getByText("已记录未归属费用", { exact: true }).locator("xpath=..");
      await expect(card.locator(":scope > div").last()).toContainText(
        `${format.format(new Date(view.start_at))} 至 ${format.format(new Date(view.end_at))}`,
      );
      const layout = await card.evaluate((left, text) => {
        const right = left.nextElementSibling, leftSubtitle = left.lastElementChild, rightSubtitle = right?.lastElementChild;
        if (!right || !leftSubtitle || !rightSubtitle) throw new Error("Expected two adjacent statistic cards with subtitles");
        const subtitles = [leftSubtitle, rightSubtitle];
        const probes = subtitles.map(subtitle => subtitle.appendChild(document.createTextNode(" " + text)));
        try {
          const a = left.getBoundingClientRect(), b = right.getBoundingClientRect();
          return {
            twoColumns: Math.abs(a.top - b.top) <= 1 && a.right <= b.left,
            wrapped: probes.every(probe => {
              const range = document.createRange();
              range.selectNodeContents(probe);
              return range.getClientRects().length > 1;
            }),
            fits: [left, right, ...subtitles].every(element => element.scrollWidth <= element.clientWidth),
            documentWidth: document.documentElement.scrollWidth,
          };
        } finally {
          for (const probe of probes) probe.remove();
        }
      }, "W".repeat(40));
      expect(layout).toMatchObject({ twoColumns: true, wrapped: true, fits: true });
      expect(layout.documentWidth).toBeLessThanOrEqual(390);
    });

    await test.step("F-13 console tables share the saved zone, compact text and exact timestamp", async () => {
      const zone = "America/New_York";
      const full = new Intl.DateTimeFormat("zh-CN", { timeZone: zone, dateStyle: "short", timeStyle: "medium", hourCycle: "h23" });
      const checkTable = async () => {
        const stamp = page.getByRole("main").locator("table time").first();
        await expect(stamp).toHaveText(/^\d{2}-\d{2} \d{2}:\d{2}$/);
        const raw = await stamp.getAttribute("datetime");
        if (!raw) throw new Error("Missing original table timestamp");
        expect(raw).toMatch(/^\d{4}-\d{2}-\d{2}T.*Z$/);
        expect((await stamp.getAttribute("title"))?.split("\n").slice(0, 2)).toEqual([`${full.format(new Date(raw))} · ${zone}`, raw]);
        const headerZone = page.getByText(`统计时区：${zone}`, { exact: true });
        await expect(headerZone).toHaveCount(1);
        await expect(page.getByRole("banner").getByText(`统计时区：${zone}`, { exact: true })).toBeVisible();
      };
      await open("/console/usage");
      await tab(page, "费用明细").click();
      await checkTable();
      await page.setViewportSize({ width: 1280, height: 860 });
      await open("/console/security");
      await checkTable();
    });
  } finally {
    await readerContext.close();
  }
});

test("AC7 export, restore the saved data and sign in again", async ({ page, browser }) => {
  const errors: string[] = [];
  let invalidatingSessions = false;
  watchErrors(page, errors, () => invalidatingSessions);
  const readerContext = await browser.newContext();
  const reader = await readerContext.newPage();
  watchErrors(reader, errors, () => invalidatingSessions);
  const open = (path: string) => page.goto(app.origin + path);

  try {
    // Start with a quiet database that has both an administrator and a distinct active reader.
    await firstLogin(page);
    await open("/console/users");
    await (await userForm(page, ordinary)).getByRole("button", { name: "保存", exact: true }).click();
    await expect(row(page, ordinary)).toBeVisible();
    await login(reader, ordinary, app.userPassword);
    await expect(reader).toHaveURL(/\/console$/);

    await open("/console/backup");
    const include = page.getByRole("switch", { name: /包含加密后的秘密/ });
    await expect(button(page, "立即备份")).toBeEnabled();
    if (await include.getAttribute("aria-checked") === "false") await include.click();
    await download(page, () => button(page, "立即备份").click(), "original.spbak");

    await open("/console/users");
    const user = await userForm(page, afterExport);
    await user.getByRole("button", { name: "保存", exact: true }).click();
    await expect(user).toBeHidden();
    await expect(row(page, afterExport)).toBeVisible();

    await open("/console/backup");
    await expect(button(page, "立即备份")).toBeEnabled();
    await page.locator('input[type="file"]').setInputFiles(join(app.work, "original.spbak"));
    await button(page, "预检").click();
    const before = page.getByRole("row").filter({ hasText: "恢复前" });
    await download(page, () => before.getByRole("button", { name: "下载", exact: true }).click(), "before.spbak");
    await button(page, "恢复").click();
    const restore = page.getByRole("dialog");
    await restore.getByRole("checkbox", { name: "我已将完整的恢复前副本保存到本机" }).check();
    await restore.getByLabel(/请输入.*恢复备份/).fill("恢复备份");
    invalidatingSessions = true;
    const [applied] = await Promise.all([
      page.waitForResponse(response => response.url().includes("/api/v1/backups/") &&
        response.url().endsWith("/apply") && response.request().method() === "POST"),
      button(page, "开始恢复").click(),
    ]);
    const { state, reason, code } = await applied.json();
    expect(state, reason || code).toBe("applied");
    await expect(page).toHaveURL(/\/login\?restore=/, { timeout: 30_000 });
    expect((await reader.request.get(app.origin + "/api/v1/access")).status()).toBe(401);
    await login(page, "admin", app.adminPassword, new URL(page.url()).pathname + new URL(page.url()).search);
    await expect(page.getByText(/已恢复 .+ 的业务快照，共 \d+ 行。请完成下方业务核对。/)).toBeVisible();
    invalidatingSessions = false;
    await open("/console/users");
    await expect(row(page, ordinary)).toBeVisible();
    await expect(row(page, afterExport)).toHaveCount(0);
    expect(errors.map(app.redact)).toEqual([]);
  } finally {
    await readerContext.close();
  }
});

test("周额度原生链路从可信估算到次周预发", async ({ page }) => {
  test.setTimeout(240_000);

  const post = async <T>(route: string, data: object) => json<T>(await page.request.post(
    app.origin + "/api/v1/" + route, { headers: { origin: app.origin }, data },
  ));
  const contract = await test.step("通过控制台接入来源、纳管、绑定参与者并授权", async () => {
    await app.browserEvent({ kind: "reset_weekly_data" });
    await firstLogin(page);
    await page.goto(app.origin + "/console/connection");
    await page.getByLabel(/服务地址/).fill(app.fixtureOrigin);
    await page.getByPlaceholder("输入管理凭据").fill(fixtureKey);
    await button(page, "保存").click();
    await expect(page.getByText("已配置（原文不可读取）", { exact: true })).toBeVisible();
    await button(page, "重新读取").click();
    const [diagnosis] = await Promise.all([
      page.waitForResponse(response => response.url().endsWith("/api/v1/connection/test") && response.request().method() === "POST"),
      button(page, "测试连接").click(),
    ]);
    expect(await json(diagnosis)).toMatchObject({ version: "0.2.23", saved: true });
    await tab(page, "能力检查").click();
    await button(page, "开启钱包授权").click();
    await page.getByLabel("当前管理员密码", { exact: true }).fill(app.adminPassword);
    await button(page, "确认开启").click();
    await expect(button(page, "撤销钱包授权")).toBeVisible();
    await setSchedule(page, false);
    await tab(page, "账号纳管").click();
    await button(page, "纳管").click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("textbox").fill(account);
    await dialog.getByRole("button", { name: /保存|确认|纳管/ }).click();
    await expect(dialog).toBeHidden();

    await page.goto(app.origin + "/console/pools");
    await tab(page, "参与者").click();
    await memberForm(page, member, "fixture@example.invalid");
    await tab(page, "额度池与份额").click();
    await button(page, "新建额度池").click();
    await dialog.getByLabel("池名称", { exact: true }).fill(pool);
    await dialog.getByRole("checkbox", { name: /^Browser account · 1$/ }).check();
    await dialog.getByRole("spinbutton", { name: member + " 份额" }).fill("40");
    const [saved] = await Promise.all([
      page.waitForResponse(response => response.url().endsWith("/api/v1/pools/create") && response.request().method() === "POST"),
      button(page, "创建额度池").click(),
    ]);
    return json<{ id: string; pool_id: string; source_id: string; connection_revision: string;
      accounts: { id: string }[]; participants: { id: string; share_percent: string }[] }>(saved);
  });

  const accountId = contract.accounts[0].id;
  const participantId = contract.participants[0].id;
  expect(contract.participants[0].share_percent).toBe("40");

  let connectionRevision = contract.connection_revision;
  const status = () => post<{ revision: string; pool_revision: string; entries: unknown[]; issuances?: unknown[] }>(
    "pools/weekly-wallet", { id: contract.pool_id },
  );
  const configure = async (autoAllocate: boolean) => {
    const { revision, pool_revision } = await status();
    expect(await post("pools/weekly-wallet/configure", {
      id: contract.pool_id, revision, pool_revision, enabled: true, auto_allocate: autoAllocate,
    })).toMatchObject({ state: "enabled", auto_allocate: autoAllocate });
  };
  const estimate = () => post<{ native_weekly: {
    observation: { used_percent: string } | null;
    latest: { id: string; connection_revision: string; observation: { observation_id: string } } | null;
  } | null }>(
    "estimates/current", { account_id: accountId },
  );
  const recommendation = () => post("pools/weekly-wallet/recommendation", { id: participantId });
  const user = async () => (await fixtureRead<{ data: { balance: number; frozen_balance: number } }>("users/21")).data;
  const ledger = async () => (await fixtureRead<{ data: { items: { value: number; notes: string }[] } }>(
    "users/21/balance-history?page=1&page_size=20",
  )).data.items;
  const adjustFixtureBalance = async (operation: "add" | "subtract", balance: number, notes: string) => {
    const result = await fetch(app.fixtureOrigin + "/api/v1/admin/users/21/balance", {
      method: "POST", signal: AbortSignal.timeout(5000),
      headers: { "X-API-Key": fixtureKey, "Content-Type": "application/json" },
      body: JSON.stringify({ operation, balance, notes }),
    });
    expect(result.status, notes).toBe(200);
  };
  const observe = (usedPercent: number, at: number, resetAt: number, cost = false) => app.browserEvent({
    kind: "observe", used_percent: usedPercent, at: new Date(at).toISOString(), reset_at: new Date(resetAt).toISOString(),
    ...(cost ? { total_cost: 4, actual_cost: 2.4 } : {}),
  });
  const collect = async (usedPercent: number, at: number, resetAt: number, cost = false) => {
    await observe(usedPercent, at, resetAt, cost);
    await app.advanceCollectionCooldown(accountId);
    const { id } = await post<{ id: string }>("monitoring/collect", {
      source_id: contract.source_id, connection_revision: connectionRevision, all: false, account_ids: [accountId],
    });
    await expect.poll(() => post("monitoring/runs/get", { id, after: "0", limit: 10 }), {
      message: "passive observation " + usedPercent + "%", timeout: 20_000,
    }).toMatchObject({ jobs: [{ state: "succeeded", reason: "VERIFIED_PASSIVE_SNAPSHOT" }] });
  };
  const trusted = () => expect.poll(estimate, { timeout: 40_000 }).toMatchObject({
    native_weekly: {
      state: "ready",
      latest: {
        capacity: { numerator: "50", denominator: "1" },
        cumulative: {
          confidence: "trusted",
          checkpoints: ["8", "16", "24"].map(used_percent => ({ observation: { used_percent } })),
        },
      },
      allocation: { pool_id: contract.pool_id, contract_id: contract.id, members: [
        { participant_id: participantId, share_percent: "40", capacity: { numerator: "20", denominator: "1" } },
      ] },
    },
  });
  const applied = (phase: string, amount: string) => expect.poll(async () => (await status()).issuances, {
    message: "confirmed weekly wallet " + phase, timeout: 40_000,
  }).toEqual(expect.arrayContaining([expect.objectContaining({
    phase, state: "applied", application_id: expect.any(String), issuance: expect.objectContaining({ amount }),
  })]));

  await configure(false);
  await page.goto(app.origin + "/console/connection");
  const now = Date.now();
  const publishedAt = now - 150_000;
  const reset = now + 100_000;
  const nextReset = reset + 7 * 86_400_000;

  await test.step("冻结时建立并验证可信估算", async () => {
    await observe(0, publishedAt - 30_000, reset);
    await app.browserEvent({ kind: "freeze", frozen_balance: 1 });
    await setSchedule(page, true);
    await expect.poll(async () => (await estimate()).native_weekly?.observation?.used_percent, { timeout: 20_000 }).toBe("0");
    for (const [index, percent] of [8, 16, 24].entries()) {
      await collect(percent, publishedAt - (2 - index) * 10_000, reset, true);
    }
    await trusted();
    expect(await user()).toMatchObject({ balance: 0, frozen_balance: 1 });
    expect(await recommendation()).toMatchObject({
      state: "ready", proposal: { delta: "12", issuance: { balance: "0", frozen: "1" } },
    });
    expect(await ledger()).toHaveLength(0);
  });

  await test.step("C1 同址保存与凭据轮换保留估算、合同和发放设置", async () => {
    const original = (await estimate()).native_weekly?.latest;
    expect(original?.id).toEqual(expect.any(String));
    expect(original?.observation.observation_id).toEqual(expect.any(String));
    const grants = async () => (await json<{ grants: { capability: string; grant: { id: string } | null }[] }>(
      await page.request.get(app.origin + "/api/v1/connection/write-grants"),
    )).grants.find(row => row.capability === "wallet")?.grant?.id;
    const originalGrant = await grants();
    expect(originalGrant).toEqual(expect.any(String));
    const originalControl = await status();
    for (const [credential_action, credential] of [["keep", ""], ["replace", fixtureBearer]]) {
      const saved = await post<{ source_changed: boolean; configuration: { source_id: string; revision: string } }>("connection/save", {
        revision: connectionRevision, base_url: app.fixtureOrigin,
        credential_action, credential, confirmation: "",
      });
      expect(saved).toMatchObject({ source_changed: false, configuration: { source_id: contract.source_id } });
      expect(saved.configuration.revision).not.toBe(connectionRevision);
      connectionRevision = saved.configuration.revision;
      expect(await status()).toMatchObject({
        state: "enabled", enabled: true, auto_allocate: false,
        revision: originalControl.revision, pool_revision: originalControl.pool_revision,
      });
      expect(await grants()).toBe(originalGrant);
      await collect(24, publishedAt, reset);
      await trusted();
      expect((await estimate()).native_weekly?.latest).toMatchObject({
        id: original?.id, connection_revision: original?.connection_revision, observation: original?.observation,
      });
    }
  });

  await test.step("C1 已暂停的旧设置由管理员保存原开关恢复", async () => {
    await app.useLegacyWeeklyWalletControl(contract.pool_id);
    expect(await status()).toMatchObject({ state: "paused", enabled: true, auto_allocate: false, entries: [] });
    await page.goto(app.origin + "/console/pools");
    await tab(page, "额度池与份额").click();
    await button(page, "周额度发放").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("待重新确认", { exact: true })).toBeVisible();
    const [saved] = await Promise.all([
      page.waitForResponse(response => response.url().endsWith("/api/v1/pools/weekly-wallet/configure") && response.request().method() === "POST"),
      dialog.getByRole("button", { name: "保存设置", exact: true }).click(),
    ]);
    expect(await json(saved)).toMatchObject({ state: "enabled", enabled: true, auto_allocate: false });
    expect(await ledger()).toHaveLength(0);
  });

  await test.step("同周回落和闲置后继续显示可信容量与份额", async () => {
    const published = (await estimate()).native_weekly?.latest;
    expect(published?.id).toEqual(expect.any(String));
    await collect(23.9, publishedAt + 1000, reset);
    await trusted();
    const { saved_at: _, ...config } = await json<Record<string, unknown>>(await page.request.get(app.origin + "/api/v1/monitoring/config"));
    await post("monitoring/config/update", {
      ...config, interval_seconds: 60, stale_after_seconds: 60, near_reset_interval_seconds: 60,
    });
    await expect.poll(estimate, { timeout: 8_000 }).toMatchObject({ native_weekly: {
      state: "source_stale", observation: { used_percent: "24" }, latest: { id: published?.id }, remaining: null,
      allocation: { members: [{ participant_id: participantId, remaining: null }], reserve: { remaining: null } },
    } });
    await page.goto(app.origin + "/console/estimates");
    await expect(page.getByText("观测已过期", { exact: true })).toBeVisible();
    await expect(page.getByText("50.00", { exact: true })).toBeVisible();
    await expect(page.getByText("待更新", { exact: true })).toBeVisible();
    await button(page, "查看依据与分配").click();
    const share = page.getByRole("dialog").getByRole("row").filter({ has: page.getByText(member, { exact: true }) });
    await expect(share.getByText("20.00", { exact: true })).toBeVisible();
    await expect(share.getByText("待更新", { exact: true })).toBeVisible();
  });

  await test.step("余额冻结期间自动发放并核对丢失响应的流水", async () => {
    await app.browserEvent({ kind: "lose_balance_reply" });
    await configure(true);
    await applied("settlement", "12");
    expect(await user()).toMatchObject({ balance: 12, frozen_balance: 1 });
    expect(await ledger()).toEqual([expect.objectContaining({ value: 12, notes: expect.any(String) })]);
    await app.browserEvent({ kind: "freeze", frozen_balance: 0 });
  });

  await test.step("新鲜平值观测恢复剩余量", async () => {
    await collect(24, Date.now() - 1000, reset);
    await trusted();
    await page.goto(app.origin + "/console/estimates");
    await expect(page.getByText("38.00", { exact: true })).toBeVisible();
  });

  await test.step("保留上周确认记录并在真实周期切换后预发", async () => {
    await configure(false);
    await adjustFixtureBalance("subtract", 12, "C10 previous-week consumption");
    await expect.poll(Date.now, { timeout: 115_000 }).toBeGreaterThan(reset + 1000);
    await collect(0, reset + 100, nextReset);
    await applied("advance", "6");
    expect(await user()).toMatchObject({ balance: 6 });
    await collect(8, reset + 200, nextReset, true);
    await adjustFixtureBalance("add", 5, "C10 external top-up");
    expect(await user()).toMatchObject({ balance: 11 });
    const history = await ledger();
    expect(history.map(item => item.value).sort((a, b) => a - b)).toEqual([-12, 5, 6, 12]);
    expect(new Set(history.map(item => item.notes)).size).toBe(4);
    expect((await status()).issuances).toHaveLength(2);
  });

  await test.step("C6 外部充值、退款和扣费后仍按本周期额度补发", async () => {
    for (const [index, percent] of [16, 24].entries()) {
      await collect(percent, reset + 300 + index * 100, nextReset, true);
    }
    await trusted();
    const proposal = await post<{ pool: { id: string }; proposal: { basis: string } }>("pools/weekly-wallet/recommendation", { id: participantId });
    expect(proposal).toMatchObject({
      state: "ready", proposal: { delta: "6", issuance: { balance: "11", contributions: [{ prior_issued: "6", gross: "12", amount: "6" }] } },
    });
    await page.goto(app.origin + "/console/balances");
    const dialog = page.getByRole("dialog");
    await row(page, member).getByRole("button", { name: "周额度建议", exact: true }).click();
    await expect(dialog.getByText(/本次发放 6 · 当前余额 11/)).toBeVisible();
    await adjustFixtureBalance("add", 2, "C10 external refund");
    await adjustFixtureBalance("subtract", 1, "C10 external consumption");
    await dialog.getByRole("checkbox", { name: "确认向该参与者的专用钱包发放本次额度" }).check();
    const [response] = await Promise.all([
      page.waitForResponse(r => r.url().endsWith("/api/v1/pools/weekly-wallet/apply") && r.request().method() === "POST"),
      dialog.getByRole("button", { name: "确认发放", exact: true }).click(),
    ]);
    expect(await json(response)).toMatchObject({ state: "applied", issuance: { amount: "6", balance: "12" } });
    await expect(dialog.getByText(/正式结算已确认/)).toBeVisible();
    expect(await user()).toMatchObject({ balance: 18 });
    const history = await ledger();
    expect(history.filter(item => item.notes.startsWith("sub-pool-wallet-")).map(item => item.value).sort((a, b) => a - b)).toEqual([6, 6, 12]);
    expect(new Set(history.map(item => item.notes)).size).toBe(history.length);
  });
  await test.step("C2 单次读数回落仍保留可信容量", async () => {
    await collect(23.9, reset + 600, nextReset);
    expect(await estimate()).toMatchObject({
      native_weekly: { state: "ready", latest: { cumulative: { confidence: "trusted" } } },
    });
  });
});
