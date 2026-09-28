// Shared paths/helpers between global-setup, global-teardown, and lib/app.js.
const fs = require('fs');
const net = require('net');
const path = require('path');

const REPO_ROOT = path.resolve(__dirname, '..', '..');
const RUNTIME_FILE = path.join(__dirname, '..', '.runtime.json');
const FIXTURES_DIR = path.join(__dirname, '..', 'fixtures');

// Payload canary; must match e2e/fixtures/xss.eml.
const CANARY = 'XSS-CANARY-9f3a';

// The runtime file carries what global setup produced for the workers:
// { binPath, rootDir }. Per-spec app instances live under rootDir.
function writeRuntime(info) {
  fs.writeFileSync(RUNTIME_FILE, JSON.stringify(info, null, 2));
}

function readRuntime() {
  return JSON.parse(fs.readFileSync(RUNTIME_FILE, 'utf8'));
}

// Reserve a free TCP port by binding to :0 and releasing it. Another process
// may grab it before the app does; bootApp retries on that bind failure.
function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.unref();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

// Resolve once the port accepts TCP connections; an aborted signal rejects
// early (used when the app process exits before binding).
function waitForPort(port, host, timeoutMs, signal) {
  const deadline = Date.now() + timeoutMs;
  return new Promise((resolve, reject) => {
    let timer;
    const onAbort = () => { clearTimeout(timer); reject(signal.reason || new Error('aborted')); };
    if (signal) {
      if (signal.aborted) return onAbort();
      signal.addEventListener('abort', onAbort, { once: true });
    }
    const done = (fn, v) => { if (signal) signal.removeEventListener('abort', onAbort); fn(v); };
    const attempt = () => {
      const sock = net.connect(port, host);
      sock.once('connect', () => { sock.end(); done(resolve); });
      sock.once('error', () => {
        sock.destroy();
        if (signal && signal.aborted) return;
        if (Date.now() > deadline) done(reject, new Error(`port ${port} not up within ${timeoutMs}ms`));
        else timer = setTimeout(attempt, 150);
      });
    };
    attempt();
  });
}

// Signal a detached app's whole process group, falling back to the pid alone.
function killTree(pid, signal) {
  try {
    process.kill(-pid, signal);
  } catch {
    try { process.kill(pid, signal); } catch { /* already gone */ }
  }
}

function isAlive(pid) {
  try { process.kill(pid, 0); return true; } catch { return false; }
}

// SIGTERM, wait for exit up to timeoutMs, then SIGKILL whatever is left.
async function stopProcess(pid, timeoutMs = 5_000) {
  if (!pid || !isAlive(pid)) return;
  killTree(pid, 'SIGTERM');
  const deadline = Date.now() + timeoutMs;
  while (isAlive(pid) && Date.now() < deadline) {
    await new Promise((res) => setTimeout(res, 50));
  }
  if (isAlive(pid)) killTree(pid, 'SIGKILL');
}

module.exports = {
  REPO_ROOT, RUNTIME_FILE, FIXTURES_DIR, CANARY,
  writeRuntime, readRuntime, freePort, waitForPort, killTree, isAlive, stopProcess,
};
