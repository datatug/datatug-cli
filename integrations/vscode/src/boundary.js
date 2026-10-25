'use strict';

const fs = require('node:fs/promises');
const path = require('node:path');
const { spawn } = require('node:child_process');

const MAX_OUTPUT = 1024 * 1024;
const HOME = 'https://datatug.app';
class SafeError extends Error {}
function inside(root, target) {
  const relative = path.relative(root, target);
  return relative === '' || (!relative.startsWith(`..${path.sep}`) && relative !== '..' && !path.isAbsolute(relative));
}
function identifier(value) {
  return typeof value === 'string' && value.length <= 240 && value.split('/').every(s => /^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$/.test(s));
}
function label(value) {
  return String(value || '').replace(/[\x00-\x1f\x7f-\x9f\u202a-\u202e\u2066-\u2069]/g, '').slice(0, 80);
}
async function realRoots(roots) { return Promise.all(roots.map(root => fs.realpath(root))); }
async function projectPath(candidate, roots) {
  const real = await fs.realpath(candidate);
  if (!(await realRoots(roots)).some(root => inside(root, real))) throw new SafeError('Select a project physically inside a trusted workspace folder.');
  await readJSON(real, 'datatug-project.json');
  return real;
}
async function readJSON(root, relative) {
  const target = path.join(root, relative);
  const real = await fs.realpath(target);
  if (!inside(root, real)) throw new SafeError('Project metadata links outside the project are unsupported.');
  const handle = await fs.open(real, 'r');
  try {
    const stat = await handle.stat();
    if (!stat.isFile() || stat.size > 256 * 1024) throw new SafeError('Project metadata exceeds the preview size limit.');
    // Read at most limit+1 even if the file grows after stat.
    const buffer = Buffer.alloc(256 * 1024 + 1);
    const { bytesRead } = await handle.read(buffer, 0, buffer.length, 0);
    if (bytesRead > 256 * 1024) throw new SafeError('Project metadata exceeds the preview size limit.');
    return JSON.parse(buffer.subarray(0, bytesRead).toString('utf8'));
  } finally { await handle.close(); }
}
async function catalog(project) {
  let visited = 0;
  async function walk(relative, suffix, depth = 0) {
    let entries;
    try {
      const folder = path.join(project, relative);
      if ((await fs.lstat(folder)).isSymbolicLink() || !inside(project, await fs.realpath(folder))) throw new SafeError('Symbolic links in project listings are unsupported.');
      entries = await fs.readdir(folder, { withFileTypes: true });
    }
    catch (error) { if (error.code === 'ENOENT') return []; throw error; }
    const found = [];
    for (const entry of entries) {
      if (++visited > 3000 || depth > 8) throw new SafeError('Project listing exceeds the preview limit.');
      if (entry.isSymbolicLink()) throw new SafeError('Symbolic links in project listings are unsupported.');
      if (entry.isDirectory()) found.push(...await walk(path.join(relative, entry.name), suffix, depth + 1));
      else if (entry.isFile() && entry.name.endsWith(suffix)) {
        const id = path.relative(relative.startsWith('queries') ? 'queries' : 'checks', path.join(relative, entry.name.slice(0, -suffix.length))).split(path.sep).join('/');
        if (!identifier(id)) continue;
        const data = await readJSON(project, path.join(relative, entry.name));
        found.push({ id, title: label(data.title || id), hasInputs: Array.isArray(data.inputs) && data.inputs.length > 0 });
      }
    }
    return found.sort((a, b) => a.id.localeCompare(b.id));
  }
  const queries = await walk('queries', '.query.json');
  const checks = await walk('checks', '.json');
  let environments = [];
  try {
    if ((await fs.lstat(path.join(project, 'environments'))).isSymbolicLink()) throw new SafeError('Linked environments are unsupported.');
    const entries = await fs.readdir(path.join(project, 'environments'), { withFileTypes: true });
    if (entries.length > 1000) throw new SafeError('Too many environments.');
    for (const entry of entries) {
      if (entry.isSymbolicLink()) throw new SafeError('Linked environments are unsupported.');
      if (entry.isDirectory() && identifier(entry.name)) {
        await readJSON(project, path.join('environments', entry.name, 'environment-summary.json'));
        environments.push(entry.name);
      }
    }
  } catch (error) { if (error.code !== 'ENOENT') throw error; }
  environments.sort();
  return { queries, checks, environments };
}
async function executable(config, roots) {
  const explicit = config.inspect('cliPath')?.globalValue;
  if (typeof explicit !== 'string' || !path.isAbsolute(explicit)) throw new SafeError('Set DataTug: Cli Path in User settings to an absolute trusted native executable.');
  const real = await fs.realpath(explicit);
  if ((await realRoots(roots)).some(root => inside(root, real))) throw new SafeError('The CLI executable must be outside all workspace folders.');
  const stat = await fs.stat(real);
  if (!stat.isFile() || (process.platform === 'win32' ? !real.toLowerCase().endsWith('.exe') : !(stat.mode & 0o111))) throw new SafeError('The configured CLI is not a native executable file.');
  const handle = await fs.open(real, 'r');
  try {
    const magic = Buffer.alloc(4);
    await handle.read(magic, 0, 4, 0);
    if (!['7f454c46', 'feedface', 'cefaedfe', 'feedfacf', 'cffaedfe', 'cafebabe', 'bebafeca'].includes(magic.toString('hex')) && magic.subarray(0, 2).toString('ascii') !== 'MZ') throw new SafeError('Use a native CLI binary, not a shell script or wrapper.');
  } finally { await handle.close(); }
  return real;
}
function secretVariable(name) {
  return name === 'PGPASSWORD' || (typeof name === 'string' && /^[A-Z][A-Z0-9_]{0,59}_URL$/.test(name) && !/^(DATATUG|NODE|LD_|DYLD_|VSCODE|ELECTRON|GIT|SSH)/.test(name));
}
function childEnvironment(base, secret) {
  const env = { ...base };
  for (const key of Object.keys(env)) if (/^(NODE_OPTIONS|LD_PRELOAD|LD_LIBRARY_PATH|DYLD_.*)$/i.test(key)) delete env[key];
  if (secret) {
    if (!secretVariable(secret.variable) || typeof secret.value !== 'string' || secret.value.includes('\0')) throw new SafeError('Invalid connection profile.');
    env[secret.variable] = secret.value;
  }
  env.DATATUG_CLIENT = '';
  env.DATATUG_TELEMETRY = '0';
  return env;
}
function argumentsFor(kind, project, environment, id, evidence) {
  if (!path.isAbsolute(project) || !identifier(environment) || !identifier(id)) throw new SafeError('Invalid project, environment or selection.');
  if (kind === 'query') return ['query', 'run', '--project', project, '--query', id, '--env', environment, '--format', 'json', '--quiet'];
  if (kind !== 'check' || !path.isAbsolute(evidence || '') || inside(project, evidence)) throw new SafeError('Invalid private evidence location.');
  return ['checks', 'run', '--project', project, '--env', environment, '--check', id, '--local-owner', '--evidence-dir', evidence, '--timeout', '30s', '--warnings', 'fail', '--format', 'json'];
}
async function evidenceDirectory(storage, roots) {
  await fs.mkdir(storage, { recursive: true, mode: 0o700 });
  const real = await fs.realpath(storage);
  if ((await realRoots(roots)).some(root => inside(root, real))) throw new SafeError('Private extension storage must be outside every workspace folder.');
  await fs.chmod(real, 0o700);
  const evidence = await fs.mkdtemp(path.join(real, 'check-'));
  await fs.chmod(evidence, 0o700);
  return evidence;
}
function runCLI(file, args, options = {}) {
  return new Promise((resolve, reject) => {
    let child;
    try { child = (options.spawn || spawn)(file, args, { shell: false, cwd: options.cwd, env: childEnvironment(options.env || process.env, options.secret), windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] }); }
    catch { reject(new SafeError('Could not start the configured DataTug CLI. Verify its installation on the execution host.')); return; }
    let bytes = 0, stdout = [], reason, finished = false, killTimer, finalTimer;
    const cancel = options.token?.onCancellationRequested(() => stop('cancelled'));
    const timer = setTimeout(() => stop('timeout'), options.timeoutMs || 30000);
    function cleanup() { clearTimeout(timer); clearTimeout(killTimer); clearTimeout(finalTimer); cancel?.dispose(); }
    function fail(message) { if (!finished) { finished = true; cleanup(); reject(new SafeError(message)); } }
    function stop(value) {
      if (reason || finished) return;
      reason = value; stdout = [];
      child.kill('SIGTERM');
      killTimer = setTimeout(() => { child.kill('SIGKILL'); finalTimer = setTimeout(() => fail('CLI shutdown could not be confirmed. Check the execution host before retrying.'), 1000); }, options.killAfterMs || 1500);
    }
    function capture(chunk, keep) {
      bytes += chunk.length;
      if (bytes > (options.maxBytes || MAX_OUTPUT)) { stop('size_limit'); return; }
      if (keep && !reason) stdout.push(chunk);
    }
    child.stdout.on('data', chunk => capture(chunk, true));
    child.stderr.on('data', chunk => capture(chunk, false));
    child.on('error', () => fail('Could not start the configured DataTug CLI. Verify its installation on the execution host.'));
    child.on('close', (code, signal) => {
      if (finished) return;
      finished = true; cleanup();
      resolve({ code, signal, reason, stdout: reason ? '' : Buffer.concat(stdout).toString('utf8') });
    });
    if (options.token?.isCancellationRequested) stop('cancelled');
  });
}
const statuses = { pass: 0, fail: 1, warning: 1, unknown: 1, configuration_error: 2, not_found: 3, error: 4, access_denied: 5, timeout: 124, cancelled: 130 };
function parseResult(kind, result) {
  if (result.reason) throw new SafeError(({ cancelled: 'Execution cancelled. Private Check evidence may remain; no retry was started.', timeout: 'Execution timed out after 30 seconds. Check connectivity and credentials before retrying.', size_limit: 'Output exceeded 1 MiB. Narrow the saved query or Check before retrying.' })[result.reason]);
  let data;
  try { data = JSON.parse(result.stdout); } catch { throw new SafeError('The CLI returned no supported JSON result. Verify the candidate CLI version, credentials and project configuration.'); }
  if (kind === 'query') {
    if (result.code !== 0) throw new SafeError('Query failed. Check connection credentials, access policies and query configuration on this execution host.');
    return { status: 'completed', content: JSON.stringify(data, null, 2) };
  }
  if (data?.schemaVersion !== 'datatug.checks.run/v1' || !Object.hasOwn(statuses, data.status) || data.exitCode !== statuses[data.status] || data.exitCode !== result.code || !Array.isArray(data.checks) || data.checks.length > 100) throw new SafeError('The CLI returned an unsupported Check response. Update to the tested candidate contract.');
  const checks = data.checks.map(check => {
    if (!Number.isSafeInteger(check.index) || check.index < 1 || !Object.hasOwn(statuses, check.status) || !['completed', 'error', 'skipped', 'pending', 'running'].includes(check.executionStatus) || !['pass', 'fail', 'unknown'].includes(check.outcome) || !['', 'critical', 'error', 'warning', 'info'].includes(check.severity) || !Number.isFinite(check.durationMs) || check.durationMs < 0) throw new SafeError('The CLI returned an unsupported Check response.');
    return { index: check.index, status: check.status, executionStatus: check.executionStatus, outcome: check.outcome, severity: check.severity, durationMs: check.durationMs };
  });
  return { status: data.status, content: JSON.stringify({ schemaVersion: data.schemaVersion, status: data.status, exitCode: data.exitCode, checks }, null, 2) };
}
// No supported hosted context contract exists for this local candidate yet.
// Never accept a CLI/project-provided URL, even one with the correct hostname.
function hostedLink() { return { url: HOME, reason: 'Hosted project/query/Check/result context is unavailable for this local project. Only the hosted home will open; private evidence is not uploaded.' }; }

module.exports = { SafeError, MAX_OUTPUT, inside, identifier, label, projectPath, readJSON, catalog, executable, secretVariable, childEnvironment, argumentsFor, evidenceDirectory, runCLI, parseResult, hostedLink };
