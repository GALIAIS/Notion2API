const fs = require('fs');
const { fetch } = require('node-wreq');

(async () => {
  const input = JSON.parse(fs.readFileSync(0, 'utf8'));

  const cookieMap = new Map();
  for (const item of input.cookies || []) {
    const name = String((item && (item.name || item.Name)) || '').trim();
    if (!name) continue;
    const rawValue = item && (item.value !== undefined ? item.value : item.Value);
    cookieMap.set(name, String(rawValue == null ? '' : rawValue));
  }
  const setCookieRecord = new Map();
  const cookieJar = {
    getCookies() {
      return [...cookieMap.entries()].map(([name, value]) => ({ name, value }));
    },
    setCookie(cookie) {
      const text = String(cookie || '');
      const semi = text.indexOf(';');
      const pair = semi === -1 ? text : text.slice(0, semi);
      const eq = pair.indexOf('=');
      if (eq <= 0) return;
      const name = pair.slice(0, eq).trim();
      const value = pair.slice(eq + 1).trim();
      if (!name) return;
      cookieMap.set(name, value);
      setCookieRecord.set(name, value);
    },
  };

  const headers = {};
  for (const [key, value] of Object.entries(input.headers || {})) {
    if (key === undefined || key === null) continue;
    if (String(key).toLowerCase() === 'cookie') continue;
    headers[String(key)] = String(value == null ? '' : value);
  }

  const method = String(input.method || 'GET').toUpperCase();
  const fetchOptions = {
    method,
    browser: input.browser_profile || 'chrome_142',
    headers,
    cookieJar,
    timeout: Math.max(Number(input.request_timeout_ms || 0), 30000),
    throwHttpErrors: false,
  };
  if (typeof input.body === 'string' && input.body.length > 0) {
    fetchOptions.body = input.body;
  }
  const proxy = String(input.proxy || '').trim();
  if (proxy) fetchOptions.proxy = proxy;

  const result = { status: 0, content_type: '', headers: {}, body: '', set_cookies: [] };
  let response;
  try {
    response = await fetch(String(input.url || ''), fetchOptions);
  } catch (err) {
    process.stderr.write((err && err.stack ? err.stack : String(err)) + '\n');
    process.exit(2);
    return;
  }

  result.status = response.status;
  if (response.headers && typeof response.headers.forEach === 'function') {
    response.headers.forEach((value, key) => {
      const lk = String(key).toLowerCase();
      if (lk === 'set-cookie') return;
      result.headers[lk] = String(value);
    });
  }
  result.content_type = result.headers['content-type'] || '';
  result.body = await response.text();
  result.set_cookies = [...setCookieRecord.entries()].map(([name, value]) => ({ Name: name, Value: value }));
  process.stdout.write(JSON.stringify(result));
})().catch((error) => {
  process.stderr.write((error && error.stack ? error.stack : String(error)) + '\n');
  process.exit(1);
});
