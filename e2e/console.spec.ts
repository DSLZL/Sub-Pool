import { join } from "node:path";
import { expect, test, type APIResponse, type Page } from "@playwright/test";
import { fixtureKey, startServices } from "./services";

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

async function json<T>(response: APIResponse): Promise<T> {
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
      const status = page.getByRole("status", { name: "余额申请状态" });
      await expect(status).toContainText("本页余额申请 · 暂不可发起");
      await expect(page.getByText(/本页钱包尚未取得读数.*余额与冻结额读数.*应用记录/)).toBeVisible();
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
      await expect(status).toContainText("本页余额申请 · 可发起");
      await expect(status).toContainText("派发前会重新核对来源授权和钱包状态");
      await expect(page.getByText("能力门禁：余额申请", { exact: true })).toHaveCount(0);
      await wallet.getByTitle("对当前 Sub2API 用户提交原生调账").click();
      const dialog = page.getByRole("dialog");
      await dialog.getByLabel(/^余额增减额/).fill("1.25");
      await dialog.getByRole("checkbox", { name: "确认调账用户和余额增减额。" }).check();
      await button(page, "确认调账").click();
      await expect(dialog.getByText(/原生调账已核对流水 #1和独立余额读数/)).toBeVisible();
      await expect(dialog.getByText("101.25", { exact: true })).toBeVisible();
      const ledger = await fixtureRead<{ data: { total: number; items: { value: number }[] } }>("users/21/balance-history?page=1&page_size=10");
      expect(ledger.data.total).toBe(1);
      expect(ledger.data.items[0].value).toBe(1.25);
    });

    await test.step("AC6 grant a subset and verify ordinary-user data and route access", async () => {
      await open("/console/users");
      const dialog = await userForm(page, ordinary);
      await tab(page, "功能权限").click();
      await dialog.getByRole("checkbox", { name: "账号与额度", exact: true }).check();
      await dialog.getByRole("checkbox", { name: "参与者与额度池", exact: true }).check();
      await dialog.getByRole("checkbox", { name: "余额建议与应用", exact: true }).check();
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

    await test.step("F-8 wallet details use only current authorized account names", async () => {
      const catalogPath = "/api/v1/accounts/list";
      const { accounts } = await json<{ accounts: { id: string; name: string }[] }>(
        await page.request.post(app.origin + catalogPath, {
          headers: { origin: app.origin }, data: { after: "", limit: 100, search: account },
        }),
      );
      const accountID = accounts.find(item => item.name === account)?.id;
      if (!accountID) throw new Error("Managed fixture account is missing");
      const drawer = reader.getByRole("dialog");
      const label = drawer.getByTitle(accountID, { exact: true });
      const openWallet = () => row(reader, member).getByText(member, { exact: true }).click();
      let omitName = true;
      let releaseCatalog: () => void = () => {};
      const catalogDelivery = new Promise<void>(resolve => { releaseCatalog = resolve; });
      await reader.route("**" + catalogPath, async route => {
        const response = await route.fetch();
        if (omitName && response.ok()) {
          await route.fulfill({ response, json: { ...await response.json(), accounts: [], next_after: null } });
          return;
        }
        await catalogDelivery;
        await route.fulfill({ response });
      });
      try {
        await reader.goto(app.origin + "/console/balances");
        await openWallet();
        await expect(label).toHaveText("账号名称未收录");
        await expect(reader.getByRole("status", { name: "余额申请状态" })).toHaveCount(0);
        omitName = false;
        await reader.reload();
        await openWallet();
        await expect(label).toHaveText("正在读取账号名称…");
        releaseCatalog();
        await expect(label).toHaveText(account);
      } finally {
        releaseCatalog();
        await reader.unroute("**" + catalogPath);
      }

      await row(page, ordinary).getByRole("button", { name: "编辑", exact: true }).click();
      const editor = page.getByRole("dialog");
      await tab(page, "功能权限").click();
      await editor.getByRole("checkbox", { name: "账号与额度", exact: true }).uncheck();
      await editor.getByRole("button", { name: "保存", exact: true }).click();
      await expect(editor).toBeHidden();
      await reader.evaluate(() => window.dispatchEvent(new StorageEvent("storage", { key: "sp.auth.invalidate", newValue: "refresh:f8" })));
      await expect(drawer).toBeHidden();
      await openWallet();
      await expect(label).toHaveText("未授予账号读取权限。");
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

    await test.step("AC8 console links, 390px and browser errors", async () => {
      const links = [
        ["", "概览"], ["/accounts", "账号与额度"], ["/usage", "用量与费用"], ["/observations", "观测记录"],
        ["/estimates", "容量估算"], ["/pools", "参与者与额度池"], ["/balances", "余额建议与应用"],
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
        await expect(main.getByText("规划中", { exact: true })).toBeVisible();
        await expect(button(page, "启用前核验")).toHaveCount(0);
        await expect(tab(page, "当前轮次")).toHaveCount(0);
        await main.getByRole("link", { name: "帮助与公告", exact: true }).click();
        await expect(page.getByRole("button", { name: /临时放宽（规划中）/ })).toBeVisible();
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
      await expect(page.getByRole("button", { name: /临时放宽（规划中）/ })).toBeVisible();
      expect(demoRequests).toEqual([]);
    });

    await test.step("模拟状态保留临时放宽入口与核验弹窗", async () => {
      await page.setViewportSize({ width: 1280, height: 860 });
      await page.route("**/api/v1/temporary/status", route => route.fulfill({ json: {
        state: "idle", reason: "TEMPORARY_PREFLIGHT_REQUIRED", can_start: false,
        round_state: "idle", rights_state: "none", reminders_available: false,
        simulation: { protocol: "sub-pool-simulator-v1", environment: "simulated", epoch: "AAAAAAAAAAAAAAAAAAAAAA" },
      } }));
      await page.route("**/api/v1/temporary/history", route => route.fulfill({ json: { rounds: [], next_after: null } }));
      await open("/console");
      await expect(page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "临时放宽", exact: true })).toBeVisible();
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
      await page.getByRole("main").locator('a[href="/console/temporary"]').click();
      await expect(tab(page, "当前轮次")).toBeVisible();
      await expect(page.getByRole("main")).not.toContainText("S2A-");
      await button(page, "启用前核验").click();
      await expect(button(page, "读取完整范围并核验")).toBeVisible();
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
    await expect(page.getByText(/服务端已完成本地恢复。来源快照/)).toBeVisible();
    invalidatingSessions = false;
    await open("/console/users");
    await expect(row(page, ordinary)).toBeVisible();
    await expect(row(page, afterExport)).toHaveCount(0);
    expect(errors.map(app.redact)).toEqual([]);
  } finally {
    await readerContext.close();
  }
});
