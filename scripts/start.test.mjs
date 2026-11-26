import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { copyFile, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import test from "node:test";

async function fixture(t, failingService = "", args = []) {
  const root = await mkdtemp(join(tmpdir(), "sub-pool-start-"));
  const failure = join(root, "fail");
  await mkdir(join(root, "frontend/web"), { recursive: true });
  await copyFile(new URL("../start.mjs", import.meta.url), join(root, "start.mjs"));
  // A real executable keeps this lifecycle test portable to Windows.
  await copyFile(process.execPath, join(root, process.platform === "win32" ? "sub-pool.exe" : "sub-pool"));
  await writeFile(join(root, "migrate"), "process.exit(0);");
  for (const [name, file] of [["backend", "serve"], ["frontend", "frontend/web/server.js"]]) {
    const lifecycle = name === failingService ? `
      setInterval(() => {
        if (fs.existsSync(${JSON.stringify(failure)})) process.exit(7);
      }, 25);
    ` : "setInterval(() => {}, 1000);";
    await writeFile(join(root, file), `
      const fs = require('node:fs');
      const ready = ${JSON.stringify(join(root, name + ".json"))};
      fs.writeFileSync(ready + '.tmp', JSON.stringify({
        pid: process.pid, listen: process.env.SUB_POOL_LISTEN,
        origin: process.env.SUB_POOL_BACKEND_ORIGIN, host: process.env.HOSTNAME, port: process.env.PORT
      }));
      fs.renameSync(ready + '.tmp', ready);
      ${lifecycle}
    `);
  }
  const child = spawn(process.execPath, [join(root, "start.mjs"), ...args], { stdio: "ignore", env: {
    ...process.env, SUB_POOL_LISTEN: "0.0.0.0:9999", SUB_POOL_BACKEND_ORIGIN: "https://unrelated.invalid",
    SUB_POOL_HOST: "127.0.0.1", SUB_POOL_PORT: "3099",
  } });
  const closed = once(child, "close");
  t.after(async () => {
    if (child.exitCode === null && child.signalCode === null) {
      child.kill();
      await closed;
    }
    await rm(root, { recursive: true, force: true });
  });
  async function ready(name) {
    for (let attempt = 0; attempt < 200; attempt++) {
      try { return JSON.parse(await readFile(join(root, name + ".json"), "utf8")); }
      catch (error) { if (error.code !== "ENOENT") throw error; }
      await delay(25);
    }
    assert.fail(`${name} did not start`);
  }
  return { child, closed, ready, root, failChild: () => writeFile(failure, "") };
}

function exited(pid) {
  assert.throws(() => process.kill(pid, 0), { code: "ESRCH" });
}

test("one-off backend commands preserve success without starting the frontend", { timeout: 20_000 }, async t => {
  const app = await fixture(t, "", ["migrate"]);
  assert.equal((await app.closed)[0], 0);
  await assert.rejects(readFile(join(app.root, "frontend.json")), { code: "ENOENT" });
});

for (const failed of ["backend", "frontend"]) {
  test(`${failed} failure terminates its sibling and preserves failure status`, { timeout: 20_000 }, async t => {
    const app = await fixture(t, failed);
    const backend = await app.ready("backend");
    const frontend = await app.ready("frontend");
    assert.equal(backend.listen, "127.0.0.1:8081");
    assert.equal(frontend.origin, "http://127.0.0.1:8081");
    assert.equal(frontend.host, "127.0.0.1");
    assert.equal(frontend.port, "3099");
    await app.failChild();
    assert.equal((await app.closed)[0], 7);
    exited(backend.pid);
    exited(frontend.pid);
  });
}

test("SIGTERM stops both services without leaving children", { skip: process.platform === "win32", timeout: 20_000 }, async t => {
  const app = await fixture(t);
  const backend = await app.ready("backend");
  const frontend = await app.ready("frontend");
  app.child.kill("SIGTERM");
  assert.equal((await app.closed)[0], 0);
  exited(backend.pid);
  exited(frontend.pid);
});
