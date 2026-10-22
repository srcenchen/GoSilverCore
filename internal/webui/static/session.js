'use strict';
// A page can outlive the process that issued its token. Only an explicit
// authentication rejection is retried; an uncertain mutation is never replayed.
window.GoSilverSession = (() => {
  const meta = document.querySelector('meta[name="gosilver-token"]');
  let token = meta.content;
  let refreshing = null;
  async function refresh() {
    if (refreshing) return refreshing;
    refreshing = (async () => {
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), 5000);
      try {
        const response = await fetch('/api/session', { cache: 'no-store', credentials: 'same-origin', signal: controller.signal });
        if (!response.ok) throw new Error('服务暂不可用');
        const session = await response.json();
        if (typeof session.token !== 'string' || !session.token) throw new Error('无法恢复页面会话');
        token = session.token;
        meta.content = token;
        return token;
      } finally { clearTimeout(timer); }
    })().finally(() => { refreshing = null; });
    return refreshing;
  }
  async function api(path, body, signal) {
    const controller = new AbortController();
    const abort = () => controller.abort();
    if (signal) { if (signal.aborted) abort(); else signal.addEventListener('abort', abort, { once: true }); }
    const timer = setTimeout(abort, 15000);
    try {
      for (let attempt = 0; attempt < 2; attempt++) {
        if (controller.signal.aborted) throw new DOMException('请求已取消', 'AbortError');
        const response = await fetch(`/api/${path}`, {
          method: body === undefined ? 'GET' : 'POST', credentials: 'same-origin',
          headers: { 'X-GoSilver-Token': token, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
          body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal
        });
        const result = await response.json();
        if (response.status === 403 && result.code === 'session_expired' && attempt === 0) { await refresh(); continue; }
        if (!response.ok) throw new Error(result.error || `请求失败 (${response.status})`);
        return result;
      }
    } finally { clearTimeout(timer); if (signal) signal.removeEventListener('abort', abort); }
  }
  function connectEvents(onState, onConnection) {
    let source = null, timer = null, stopped = false, attempts = 0;
    const retry = () => {
      if (stopped || timer !== null) return;
      timer = setTimeout(async () => {
        timer = null;
        try { await refresh(); open(); } catch { retry(); }
      }, Math.min(500 * 2 ** Math.min(attempts++, 5), 10000));
    };
    function open() {
      if (stopped) return;
      source?.close();
      source = new EventSource(`/api/events?token=${encodeURIComponent(token)}`);
      source.onopen = () => { attempts = 0; onConnection(true); };
      source.onmessage = event => { try { onState(JSON.parse(event.data)); } catch (error) { console.error('状态渲染失败', error); } };
      source.onerror = () => { source?.close(); onConnection(false); retry(); };
    }
    open();
    return { close() { stopped = true; source?.close(); if (timer !== null) clearTimeout(timer); } };
  }
  return Object.freeze({ api, refresh, connectEvents, getToken: () => token });
})();
