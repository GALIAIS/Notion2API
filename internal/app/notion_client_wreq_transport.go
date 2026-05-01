package app

func nodeWreqHelperScript() string {
	return `const fs = require('fs');
const { fetch } = require('node-wreq');

(async () => {
  const input = JSON.parse(fs.readFileSync(0, 'utf8'));

  const cookieMap = new Map();
  for (const item of input.cookies || []) {
    const name = String((item && item.name) || '').trim();
    if (!name) continue;
    cookieMap.set(name, String((item && item.value) || ''));
  }
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
      if (name) cookieMap.set(name, value);
    },
  };

  const headers = {};
  for (const [key, value] of Object.entries(input.headers || {})) {
    if (key === undefined || key === null) continue;
    if (String(key).toLowerCase() === 'cookie') continue;
    headers[String(key)] = String(value == null ? '' : value);
  }

  const fetchOptions = {
    method: 'POST',
    browser: input.browser_profile || 'chrome_142',
    headers,
    body: JSON.stringify(input.payload || {}),
    cookieJar,
    timeout: Math.max(Number(input.request_timeout_ms || 0), 30000),
    throwHttpErrors: false,
  };
  const proxy = String(input.proxy || '').trim();
  if (proxy) fetchOptions.proxy = proxy;

  const result = { status: 0, content_type: '', text: '' };
  let response;
  try {
    response = await fetch(input.run_url, fetchOptions);
  } catch (err) {
    process.stderr.write((err && err.stack ? err.stack : String(err)) + '\n');
    process.exit(2);
    return;
  }

  result.status = response.status;
  result.content_type = response.headers.get('content-type') || '';
  const isNDJSON = String(result.content_type).toLowerCase().includes('application/x-ndjson');
  if (!isNDJSON) {
    result.text = await response.text();
    process.stdout.write(JSON.stringify(result));
    return;
  }

  const idleAfterAnswerMs = Math.max(Number(input.idle_after_answer_ms || 0), 0);
  const readable = response.wreq && typeof response.wreq.readable === 'function'
    ? response.wreq.readable()
    : null;
  if (!readable) {
    result.text = await response.text();
    process.stdout.write(JSON.stringify(result));
    return;
  }

  let pending = '';
  let sawAnswer = false;
  let sawTerminal = false;
  let settled = false;
  let idleTimer = null;

  const markLineState = (line) => {
    if (!line) return;
    try {
      const parsed = JSON.parse(line);
      if (String(parsed.type || '').toLowerCase() !== 'agent-inference' || !Array.isArray(parsed.value)) return;
      const hasVisibleText = parsed.value.some((entry) => {
        const t = String((entry && entry.type) || '').toLowerCase();
        const c = String((entry && entry.content) || '');
        return t === 'text' && c.trim() !== '';
      });
      if (!hasVisibleText) return;
      sawAnswer = true;
      if (parsed.finishedAt != null) sawTerminal = true;
    } catch (_) {}
  };

  await new Promise((resolve, reject) => {
    const settle = () => {
      if (settled) return;
      settled = true;
      if (idleTimer) {
        clearTimeout(idleTimer);
        idleTimer = null;
      }
      const remaining = pending.trim();
      if (remaining) markLineState(remaining);
      try { readable.destroy(); } catch (_) {}
      resolve();
    };
    const armIdle = () => {
      if (idleTimer) {
        clearTimeout(idleTimer);
        idleTimer = null;
      }
      if (sawAnswer && idleAfterAnswerMs > 0) {
        idleTimer = setTimeout(settle, idleAfterAnswerMs);
      }
    };
    readable.on('data', (chunk) => {
      const text = Buffer.isBuffer(chunk) ? chunk.toString('utf8') : String(chunk);
      result.text += text;
      pending += text;
      while (true) {
        const newlineIndex = pending.indexOf('\n');
        if (newlineIndex === -1) break;
        const line = pending.slice(0, newlineIndex).trim();
        pending = pending.slice(newlineIndex + 1);
        markLineState(line);
        if (sawTerminal) {
          settle();
          return;
        }
      }
      armIdle();
    });
    readable.on('end', settle);
    readable.on('close', settle);
    readable.on('error', (err) => {
      if (settled) return;
      settled = true;
      if (idleTimer) clearTimeout(idleTimer);
      reject(err);
    });
  });

  process.stdout.write(JSON.stringify(result));
})().catch((error) => {
  process.stderr.write((error && error.stack ? error.stack : String(error)) + '\n');
  process.exit(1);
});
`
}

func nodeWreqLoginHelperScript() string {
	return `const fs = require('fs');
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
`
}
