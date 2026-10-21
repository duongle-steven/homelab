// Renovate's GitHub token (docs/how-to-guides/renovate.md), one per run:
//   mint    (the CronJob's initContainer): signs a JWT with the App's key (KEY_FILE), asks GitHub for an installation
//           token with only PERMISSIONS (JSON) and writes it to TOKEN_FILE
//   revoke  (the Renovate container, when the run ends): revokes the token in TOKEN_FILE
// Prints neither the key, the JWT nor the token. Passing trouble (the network, GitHub's 5xx or 429) is retried three
// times (after 10, 30 and 90 seconds; RETRY_DELAYS in milliseconds for the tests); a refusal (4xx: the key, the App
// or a permission is wrong) fails at once with GitHub's status and message.
'use strict';
const crypto = require('node:crypto');
const fs = require('node:fs');

const api = process.env.GITHUB_API || 'https://api.github.com';
const headers = { Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' };

function env(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is not set`);
  return value;
}

function jwt(appID, key) {
  const encode = (value) => Buffer.from(JSON.stringify(value)).toString('base64url');
  const now = Math.floor(Date.now() / 1000);
  // Issued a minute early against clock drift; GitHub refuses a JWT that lives more than 10 minutes
  const unsigned = `${encode({ alg: 'RS256', typ: 'JWT' })}.${encode({ iat: now - 60, exp: now + 8 * 60, iss: appID })}`;
  return `${unsigned}.${crypto.sign('RSA-SHA256', Buffer.from(unsigned), key).toString('base64url')}`;
}

async function request(method, path, token, body) {
  const response = await fetch(`${api}${path}`, {
    method,
    headers: { ...headers, Authorization: `Bearer ${token}` },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(30_000),
  });
  const text = await response.text();
  let json = {};
  try {
    json = JSON.parse(text);
  } catch {
    // An empty body (204) or an HTML error page
  }
  return { status: response.status, json };
}

const delays = (process.env.RETRY_DELAYS || '10000,30000,90000').split(',').map(Number);

async function mint() {
  const permissions = JSON.parse(env('PERMISSIONS'));
  const key = fs.readFileSync(env('KEY_FILE'));
  let status;
  let json;
  for (let attempt = 0; ; attempt++) {
    let trouble;
    try {
      // A new JWT for every attempt: the last one may come minutes after the first
      ({ status, json } = await request('POST', `/app/installations/${env('INSTALLATION_ID')}/access_tokens`, jwt(env('APP_ID'), key), { permissions }));
      if (status === 201 || (status < 500 && status !== 429)) break;
      trouble = `${status} ${json.message ?? ''}`.trim();
    } catch (error) {
      trouble = error.message;
    }
    if (attempt === delays.length) throw new Error(`GitHub did not mint the installation token: ${trouble}`);
    console.error(`GitHub did not mint the installation token (${trouble}): retrying in ${delays[attempt] / 1000} s`);
    await new Promise((resolve) => setTimeout(resolve, delays[attempt]));
  }
  if (status !== 201 || typeof json.token !== 'string') {
    throw new Error(`GitHub refused the installation token: ${status} ${json.message ?? ''}`.trim());
  }
  fs.writeFileSync(env('TOKEN_FILE'), json.token, { mode: 0o600 });
  console.log(`installation token for ${Object.keys(permissions).sort().join(', ')}, expires ${json.expires_at}`);
}

async function revoke() {
  const { status } = await request('DELETE', '/installation/token', fs.readFileSync(env('TOKEN_FILE'), 'utf8').trim());
  if (status !== 204) throw new Error(`GitHub did not revoke the installation token: ${status}`);
  console.log('installation token revoked');
}

const commands = { mint, revoke };
const command = commands[process.argv[2]];
if (!command) {
  console.error('usage: github-token.js mint|revoke');
  process.exit(2);
}
command().catch((error) => {
  console.error(error.message);
  process.exit(1);
});
