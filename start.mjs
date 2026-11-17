import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL(".", import.meta.url));
const command = process.argv.slice(2);
const children = new Set();
let stopping = false;
let deadline;

function stop(code) {
  if (stopping) return;
  stopping = true;
  process.exitCode = code;
  for (const child of children) child.kill("SIGTERM");
  deadline = setTimeout(() => {
    for (const child of children) child.kill("SIGKILL");
  }, 10_000);
  deadline.unref();
}

process.on("SIGINT", () => stop(0));
process.on("SIGTERM", () => stop(0));

const services = [
  ["backend", fileURLToPath(new URL(process.platform === "win32" ? "sub-pool.exe" : "sub-pool", import.meta.url)), command.length ? command : ["serve"], {
    SUB_POOL_LISTEN: "127.0.0.1:8081",
  }],
];
if (command.length === 0) services.push(
  ["frontend", process.execPath, [fileURLToPath(new URL("frontend/web/server.js", import.meta.url))], {
    NODE_ENV: "production",
    HOSTNAME: process.env.SUB_POOL_HOST ?? "127.0.0.1",
    PORT: process.env.SUB_POOL_PORT ?? "3000",
    SUB_POOL_BACKEND_ORIGIN: "http://127.0.0.1:8081",
  }],
);

for (const [name, command, args, env] of services) {
  const child = spawn(command, args, { cwd: root, env: { ...process.env, ...env }, stdio: "inherit" });
  children.add(child);
  child.once("error", () => {
    console.error(`${name} could not start`);
    stop(1);
  });
  child.once("close", (code) => {
    children.delete(child);
    if (!stopping) {
      if (command.length === 0) console.error(`${name} exited unexpectedly`);
      stop(command.length ? code ?? 1 : code || 1);
    }
    if (children.size === 0) clearTimeout(deadline);
  });
}
