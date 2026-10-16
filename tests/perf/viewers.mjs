// viewers.mjs — #4028: headless Chrome dashboard viewers for profile_busy.sh. Each --tab is
// "name,width,height,base,path": a separate browser context signs in with the password on base,
// then opens path at that size and stays there until the process is killed.
import { spawn } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";

// chrome-remote-interface from the frontend tests' node_modules (--modules, a tests/frontend folder).
const modArg = process.argv.indexOf("--modules");
const modDir = modArg >= 0 ? process.argv[modArg + 1] : new URL("../frontend", import.meta.url).pathname;
const CDP = createRequire(join(modDir, "package.json"))("chrome-remote-interface");

const args = process.argv.slice(2);
const opt = (k) => { const i = args.indexOf(k); return i >= 0 ? args[i + 1] : ""; };
const tabs = [];
for (let i = 0; i < args.length; i++) if (args[i] === "--tab") tabs.push(args[i + 1].split(","));
const chrome = opt("--chrome") || (process.platform === "darwin"
  ? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" : "google-chrome");
const profile = opt("--profile-dir") || mkdtempSync(join(tmpdir(), "perf-chrome-"));
const password = opt("--password");
const port = 9300 + Math.floor(Math.random() * 500);

const proc = spawn(chrome, ["--headless=new", `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
  "--no-first-run", "--no-default-browser-check", "--disable-gpu", "about:blank"], { stdio: "ignore" });
const stop = () => { try { proc.kill("SIGTERM"); } catch {} process.exit(0); };
process.on("SIGTERM", stop);
process.on("SIGINT", stop);

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function connectBrowser() {
  for (let i = 0; i < 60; i++) {
    try { return await CDP({ port }); } catch { await sleep(500); }
  }
  throw new Error("chrome did not start");
}

const browser = await connectBrowser();
for (const [name, w, h, base, path] of tabs) {
  // One browser for all: each server's session cookie has its own name (its data folder's hash).
  const { targetId } = await browser.Target.createTarget({ url: "about:blank" });
  const page = await CDP({ port, target: targetId });
  await page.Page.enable();
  await page.Emulation.setDeviceMetricsOverride({ width: +w, height: +h, deviceScaleFactor: 1, mobile: +w < 600 });
  await page.Page.navigate({ url: base + "/auth" });
  await sleep(2000);
  const res = await page.Runtime.evaluate({
    awaitPromise: true, returnByValue: true,
    expression: `fetch("/auth/password", {method: "POST", headers: {"Content-Type": "application/json"},
      body: JSON.stringify({password: ${JSON.stringify(password)}})}).then(r => r.status)`,
  });
  console.log(`[viewers] ${name}: sign-in ${res.result.value}; opening ${base}${path} at ${w}x${h}`);
  await page.Page.navigate({ url: base + path });
}
console.log("[viewers] all tabs open");
setInterval(() => {}, 1 << 30);
