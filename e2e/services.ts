import { execFile, spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { cp, mkdtemp, rm } from "node:fs/promises";
import { createServer, type AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { promisify } from "node:util";

export const fixtureKey = "adm_sc_test_only_secret";
export const fixtureBearer = "Bearer fixture-admin-token";
const exec = promisify(execFile);

type BrowserEvent =
  | { kind: "reset_weekly_data" | "lose_balance_reply" }
  | { kind: "observe"; at: string; reset_at: string; used_percent: number; total_cost?: number; actual_cost?: number }
  | { kind: "freeze"; frozen_balance: number };

async function loopbackPort() {
  const server = createServer();
  const port = await new Promise<number>((ok, fail) => {
    server.once("error", fail);
    server.listen(0, "127.0.0.1", () => ok((server.address() as AddressInfo).port));
  });
  await new Promise<void>((ok, fail) => server.close(error => error ? fail(error) : ok()));
  return port;
}

function start(command: string, args: string[], env: NodeJS.ProcessEnv, cwd: string) {
  const child = spawn(command, args, { env, cwd, windowsHide: true });
  let tail = "";
  let exited = false;
  const collect = (data: Buffer) => { tail = (tail + data.toString()).slice(-4000); };
  child.stdout.on("data", collect);
  child.stderr.on("data", collect);
  child.on("error", error => collect(Buffer.from(error.message)));
  child.stdin.on("error", error => collect(Buffer.from(error.message)));
  const closed = new Promise<void>(done => child.once("close", () => {
    exited = true;
    done();
  }));
  return {
    child,
    output: () => tail,
    async wait<T>(message: string, probe: () => T | undefined | Promise<T | undefined>) {
      for (const deadline = Date.now() + 30_000; Date.now() < deadline && !exited;) {
        const result = await probe();
        if (result !== undefined) return result;
        await delay(150);
      }
      throw new Error(message);
    },
    async stop() {
      if (exited) return;
      child.kill();
      await Promise.race([closed, delay(5000)]);
      if (!exited) {
        child.kill("SIGKILL");
        await closed;
      }
    },
  };
}

async function httpReady(url: string) {
  const response = await fetch(url, { signal: AbortSignal.timeout(1500) }).catch(() => null);
  return response?.ok ? true : undefined;
}

const password = () => "Smoke!" + randomBytes(12).toString("hex") + "9";

export async function startServices() {
  const input = process.env.SUB_POOL_E2E_DATABASE_URL;
  if (!input) throw new Error("Set SUB_POOL_E2E_DATABASE_URL to a local PostgreSQL test database.");
  const base = new URL(input);
  if (!["postgres:", "postgresql:"].includes(base.protocol) || base.hostname !== "127.0.0.1" ||
      !/^\/subpool_test_[a-z0-9_]+$/.test(base.pathname) ||
      [...base.searchParams.keys()].some(name => name !== "sslmode")) {
    throw new Error("SUB_POOL_E2E_DATABASE_URL needs a 127.0.0.1 subpool_test_ database.");
  }
  const dbName = "subpool_test_browser_" + randomBytes(8).toString("hex");
  const testDB = new URL(base);
  testDB.pathname = "/" + dbName;
  const root = process.cwd();
  const work = await mkdtemp(join(resolve(tmpdir()), "subpool-e2e-"));
  const goExe = (name: string) => join(work, name + (process.platform === "win32" ? ".exe" : ""));
  const env = Object.fromEntries(Object.entries(process.env).filter(([name]) => !/^(SUB_POOL_|PG)/i.test(name)));
  const initialPassword = password();
  const adminPassword = password();
  const userPassword = password();
  const secretKey = randomBytes(32).toString("base64");
  const secrets = [base.href, testDB.href, base.password, initialPassword, adminPassword, userPassword, secretKey, fixtureKey].filter(Boolean);
  const redact = (message: string) => secrets.reduce((result, value) => result.replaceAll(value, "[redacted]"), message);
  const children: ReturnType<typeof start>[] = [];
  let dbCreated = false;

  async function run(command: string, args: string[], description: string, timeout: number) {
    try {
      await exec(command, args, { cwd: root, env, timeout, windowsHide: true });
    } catch (error) {
      const result = error as { message?: string; stderr?: string };
      throw new Error(description + ": " + redact(result.stderr?.trim() || result.message || "command failed"));
    }
  }
  const sql = (statement: string, database = base.href) => run(process.env.SUB_POOL_E2E_PSQL || "psql",
    ["-X", "-v", "ON_ERROR_STOP=1", "--dbname", database, "-c", statement], "Browser test database", 45_000);

  async function stop() {
    try {
      for (const child of children.reverse()) await child.stop();
      if (dbCreated) {
        await sql("DROP DATABASE " + dbName + " WITH (FORCE)");
        dbCreated = false;
      }
    } finally {
      await rm(work, { recursive: true, force: true });
    }
  }
  const logs = () => redact(children.map(child => child.output()).filter(Boolean).join("\n").trim());

  try {
    await run("go", ["-C", "backend", "build", "-o", goExe("fakesub2api"), "./cmd/fakesub2api"], "Build Sub2API fixture", 90_000);
    await run("go", ["-C", "backend", "build", "-o", goExe("sub-pool"), "./cmd/sub-pool"], "Build Go server", 90_000);
    const standalone = join(root, "web", ".next", "standalone", "web");
    await cp(join(root, "web", ".next", "static"), join(standalone, ".next", "static"), { recursive: true });
    await sql("CREATE DATABASE " + dbName);
    dbCreated = true;

    const fixture = start(goExe("fakesub2api"), ["-browser-smoke"], env, root);
    children.push(fixture);
    const fixtureOrigin = await fixture.wait("Sub2API fixture did not bind", () => fixture.output().match(/^http:\/\/127\.0\.0\.1:\d+\r?$/m)?.[0].trim());
    await fixture.wait("Sub2API fixture did not become healthy", () => httpReady(fixtureOrigin + "/health"));
    const webPort = await loopbackPort();
    const backendPort = await loopbackPort();
    const origin = "http://127.0.0.1:" + webPort;
    const backend = start(goExe("sub-pool"), ["serve"], {
      ...env,
      SUB_POOL_DATABASE_URL: testDB.href,
      SUB_POOL_SECRET_KEY: secretKey,
      SUB_POOL_INITIAL_ADMIN_PASSWORD: initialPassword,
      SUB_POOL_SIMULATION_BINDING: "retired-fixture-setting",
      SUB_POOL_PUBLIC_ORIGIN: origin,
      SUB_POOL_ALLOW_LOOPBACK_HTTP: "true",
      SUB_POOL_LISTEN: "127.0.0.1:" + backendPort,
    }, root);
    children.push(backend);
    const backendOrigin = "http://127.0.0.1:" + backendPort;
    await backend.wait("Go did not become ready", () => httpReady(backendOrigin + "/health/ready"));

    const frontend = start(process.execPath, [join(standalone, "server.js")], {
      ...env,
      HOSTNAME: "127.0.0.1",
      PORT: String(webPort),
      NEXT_TELEMETRY_DISABLED: "1",
      SUB_POOL_BACKEND_ORIGIN: backendOrigin,
      SUB_POOL_PUBLIC_ORIGIN: origin,
      SUB_POOL_ALLOW_LOOPBACK_HTTP: "true",
    }, standalone);
    children.push(frontend);
    await frontend.wait("Next login page did not become ready", () => httpReady(origin + "/login"));
    await frontend.wait("Next could not proxy Go readiness", () => httpReady(origin + "/health/ready"));
    let eventId = 0;
    return {
      origin, fixtureOrigin, work, initialPassword, adminPassword, userPassword, stop, logs, redact,
      async recordUsage() {
        fixture.child.stdin.end("\n");
        return fixture.wait("Sub2API fixture did not acknowledge new usage", () => fixture.output().match(/^usage (\S+)$/m)?.[1]);
      },
      async browserEvent(event: BrowserEvent) {
        const ack = new RegExp("^event " + ++eventId + "(?: ([^\\r\\n]+))?\\r?\\n", "m");
        fixture.child.stdin.write(JSON.stringify(event) + "\n");
        const match = await fixture.wait("Sub2API fixture did not acknowledge " + event.kind, () => fixture.output().match(ack) ?? undefined);
        if (match[1]) throw new Error("Sub2API fixture rejected " + event.kind + ": " + match[1]);
      },
      async advanceCollectionCooldown(accountId: string) {
        if (!/^[A-Za-z0-9_-]+$/.test(accountId)) throw new Error("Invalid test account ID");
        // Match the Go collection fixture's clock advance in this run's isolated database.
        await sql("UPDATE collection_jobs SET started_at=started_at-interval '2 minutes', " +
          "retry_after=clock_timestamp()-interval '1 second' WHERE account_id='" + accountId + "' " +
          "AND state NOT IN ('queued','running')", testDB.href);
      },
      async useLegacyWeeklyWalletControl(poolId: string) {
        if (!/^[A-Za-z0-9_-]+$/.test(poolId)) throw new Error("Invalid test pool ID");
        await sql("UPDATE pools SET weekly_wallet=weekly_wallet-'source_continuity' WHERE id='" + poolId + "'", testDB.href);
      },
    };
  } catch (error) {
    const diagnostic = logs();
    await stop();
    throw new Error(redact(String(error)) + (diagnostic ? "\n" + diagnostic : ""));
  }
}
