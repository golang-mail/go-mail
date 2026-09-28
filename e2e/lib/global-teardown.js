// Stops any app a spec file left running (a crashed worker skips afterAll)
// and removes the root dir with the binary and per-spec work dirs.
const fs = require('fs');
const path = require('path');
const { RUNTIME_FILE, readRuntime, stopProcess } = require('./harness');

async function globalTeardown() {
  let info;
  try {
    info = readRuntime();
  } catch {
    return; // setup never completed
  }

  const appsDir = path.join(info.rootDir, 'apps');
  const leftovers = fs.existsSync(appsDir) ? fs.readdirSync(appsDir).filter((f) => f.endsWith('.json')) : [];
  for (const f of leftovers) {
    try {
      const app = JSON.parse(fs.readFileSync(path.join(appsDir, f), 'utf8'));
      await stopProcess(app.pid);
    } catch { /* unreadable record; nothing to stop */ }
  }

  if (process.env.E2E_KEEP) {
    console.log(`E2E_KEEP set: leaving ${info.rootDir} (per-spec app.log under app-*/) and ${RUNTIME_FILE}`);
    return;
  }
  fs.rmSync(info.rootDir, { recursive: true, force: true });
  fs.rmSync(RUNTIME_FILE, { force: true });
}

module.exports = globalTeardown;
