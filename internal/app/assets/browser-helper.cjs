const fs = require('fs');
const { fetch } = require('node-wreq');

const poolMode = String(process.env.NOTION2API_BROWSER_HELPER_MODE || '').trim().toLowerCase() === 'pool'
  && String(process.env.NOTION2API_BROWSER_HELPER_PROTOCOL || '').trim() === 'N2A_HELPER_POOL_V1';

function formatError(error) {
  return error && error.stack ? error.stack : String(error);
}

function buildCookieJar(items) {
  const cookieMap = new Map();
  for (const item of items || []) {
    const name = String((item && item.name) || '').trim();
    if (!name) continue;
    cookieMap.set(name, String((item && item.value) || ''));
  }
  return {
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
}

function buildHeaders(rawHeaders) {
  const headers = {};
  for (const [key, value] of Object.entries(rawHeaders || {})) {
    if (key === undefined || key === null) continue;
    if (String(key).toLowerCase() === 'cookie') continue;
    headers[String(key)] = String(value == null ? '' : value);
  }
  return headers;
}

function markLineState(line, state) {
  if (!line || !state) return;
  try {
    const parsed = JSON.parse(line);
    if (String(parsed.type || '').toLowerCase() !== 'agent-inference' || !Array.isArray(parsed.value)) return;
    const hasVisibleText = parsed.value.some((entry) => {
      const t = String((entry && entry.type) || '').toLowerCase();
      const c = String((entry && entry.content) || '');
      return t === 'text' && c.trim() !== '';
    });
    if (!hasVisibleText) return;
    state.sawAnswer = true;
    if (parsed.finishedAt != null) state.sawTerminal = true;
  } catch (_) {}
}

async function runSingleRequest(input) {
  const cookieJar = buildCookieJar(input.cookies || []);
  const headers = buildHeaders(input.headers || {});
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
  const response = await fetch(input.run_url, fetchOptions);
  result.status = response.status;
  result.content_type = response.headers.get('content-type') || '';
  const isNDJSON = String(result.content_type).toLowerCase().includes('application/x-ndjson');
  if (!isNDJSON) {
    result.text = await response.text();
    return result;
  }

  const idleAfterAnswerMs = Math.max(Number(input.idle_after_answer_ms || 0), 0);
  const readable = response.wreq && typeof response.wreq.readable === 'function'
    ? response.wreq.readable()
    : null;
  if (!readable) {
    result.text = await response.text();
    return result;
  }

  let pending = '';
  const state = { sawAnswer: false, sawTerminal: false };
  let settled = false;
  let idleTimer = null;

  await new Promise((resolve, reject) => {
    const settle = () => {
      if (settled) return;
      settled = true;
      if (idleTimer) {
        clearTimeout(idleTimer);
        idleTimer = null;
      }
      const remaining = pending.trim();
      if (remaining) markLineState(remaining, state);
      try { readable.destroy(); } catch (_) {}
      resolve();
    };
    const armIdle = () => {
      if (idleTimer) {
        clearTimeout(idleTimer);
        idleTimer = null;
      }
      if (state.sawAnswer && idleAfterAnswerMs > 0) {
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
        markLineState(line, state);
        if (state.sawTerminal) {
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

  return result;
}

function writeFrame(payloadBuffer) {
  const header = Buffer.allocUnsafe(4);
  header.writeUInt32LE(payloadBuffer.length, 0);
  process.stdout.write(header);
  process.stdout.write(payloadBuffer);
}

function runPoolLoop() {
  let pending = Buffer.alloc(0);
  const queue = [];
  let draining = false;

  const drainQueue = async () => {
    if (draining) return;
    draining = true;
    while (queue.length > 0) {
      const payload = queue.shift();
      let input;
      try {
        input = JSON.parse(payload.toString('utf8'));
      } catch (err) {
        process.stderr.write(formatError(err) + '\n');
        process.exit(2);
        return;
      }
      let result;
      try {
        result = await runSingleRequest(input);
      } catch (err) {
        process.stderr.write(formatError(err) + '\n');
        process.exit(2);
        return;
      }
      const body = Buffer.from(JSON.stringify(result));
      writeFrame(body);
    }
    draining = false;
  };

  process.stdin.on('data', (chunk) => {
    const incoming = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    pending = Buffer.concat([pending, incoming]);
    while (pending.length >= 4) {
      const bodyLen = pending.readUInt32LE(0);
      if (pending.length < 4 + bodyLen) {
        break;
      }
      const body = pending.subarray(4, 4 + bodyLen);
      pending = pending.subarray(4 + bodyLen);
      queue.push(body);
    }
    void drainQueue();
  });

  process.stdin.on('end', () => {
    if (pending.length !== 0) {
      process.stderr.write('incomplete pool frame at stdin end\n');
      process.exit(2);
      return;
    }
    if (!draining && queue.length === 0) {
      process.exit(0);
    }
  });
}

if (poolMode) {
  runPoolLoop();
} else {
  (async () => {
    const input = JSON.parse(fs.readFileSync(0, 'utf8'));
    const result = await runSingleRequest(input);
    process.stdout.write(JSON.stringify(result));
  })().catch((error) => {
    process.stderr.write(formatError(error) + '\n');
    process.exit(2);
  });
}
