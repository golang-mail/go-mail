// Runs once per suite: builds the binary into a throwaway root dir and records
// its path. Each spec file then boots its own app under that root via
// lib/app.js useApp().
const { spawnSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const { REPO_ROOT, writeRuntime } = require('./harness');

async function globalSetup() {
  const rootDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gomail-e2e-'));
  fs.mkdirSync(path.join(rootDir, 'apps'));

  const binPath = path.join(rootDir, 'go-mail');
  const build = spawnSync('go', ['build', '-o', binPath, '.'], {
    cwd: REPO_ROOT, encoding: 'utf8',
  });
  if (build.status !== 0) {
    fs.rmSync(rootDir, { recursive: true, force: true });
    throw new Error(`go build failed:\n${build.stdout}\n${build.stderr}`);
  }

  writeRuntime({ binPath, rootDir });
}

module.exports = globalSetup;
