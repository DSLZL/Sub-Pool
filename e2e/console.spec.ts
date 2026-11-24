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
    await button(page, label + "：输入精确 UTC").click();
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
    await test.step("AC1 first login and password change", () => firstLogin(page));

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

    await test.step("AC3 collect a closed day and display the two usage entries", async () => {
      const history = await fixtureRead<{ data: { items: { id: number; created_at: string }[] } }>("usage?account_id=1");
      expect(history.data.items.map(item => item.id).sort()).toEqual([1, 2]);
      await open("/console/pools");
      await collectDay(page, history.data.items[0].created_at);
      await expect(page.getByRole("listitem").filter({ hasText: /采集完成 · 2 \/ 2 条/ })).toBeVisible();
      await open("/console/usage");
      await tab(page, "费用明细").click();
      for (const id of [1, 2]) {
        const entry = page.getByRole("row").filter({ has: page.locator("[title^='原生行 " + id + "；']") });
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
      await dialog.getByRole("checkbox", { name: "参与者与额度池", exact: true }).check();
      await tab(page, "数据范围").click();
      await dialog.getByRole("checkbox", { name: account, exact: true }).check();
      await dialog.getByRole("checkbox", { name: member, exact: true }).check();
      await dialog.getByRole("button", { name: "保存", exact: true }).click();
      await expect(dialog).toBeHidden();
      await expect(row(page, ordinary)).toBeVisible();

      await login(reader, ordinary, app.userPassword);
      await expect(reader).toHaveURL(/\/console$/);
      const nav = reader.getByRole("navigation", { name: "主导航" });
      await nav.getByRole("link", { name: "参与者与额度池", exact: true }).click();
      await tab(reader, "参与者").click();
      await expect(row(reader, member)).toBeVisible();
      await expect(row(reader, otherMember)).toHaveCount(0);
      await expect(nav.getByRole("link", { name: "连接与采集" })).toHaveCount(0);
      await reader.goto(app.origin + "/console/connection");
      await expect(reader.getByText(/无权限|无权访问|未授予/).first()).toBeVisible();
      expect((await reader.request.get(app.origin + "/api/v1/connection")).status()).toBe(403);
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
      await page.getByRole("main").locator('a[href="/console/temporary"]').click();
      await expect(tab(page, "当前轮次")).toBeVisible();
      await expect(page.getByRole("main")).not.toContainText("S2A-");
      await button(page, "启用前核验").click();
      await expect(button(page, "读取完整范围并核验")).toBeVisible();
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
