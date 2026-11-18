// RHWP 서버 저장/불러오기 + 계정 메뉴 (rhwp-docker 추가 기능)
//
// rhwp-studio 의 확장 API(window.rhwpStudio.automation)로 [파일] 메뉴에 항목을 더한다.
// 업스트림 소스는 고치지 않는다. 서버 쪽은 rhwp-auth 의 /rhwp-auth/api/fs, /api/me.
//
// [파일] 메뉴
//  - ☁ 서버에서 열기…           : '내 문서'(files/<사용자>) 와 공유 폴더(files/mount/*) 를 폴더째 탐색해 연다
//  - ☁ 서버에 저장               : 서버에서 연(또는 저장한) 문서를 같은 자리에 덮어쓴다.
//                                  그 사이 다른 곳에서 바뀌었으면 묻는다. 아니면 '다른 이름으로'.
//  - ☁ 서버에 다른 이름으로 저장…  : 폴더를 골라 저장
//  - 👤 내 계정 / 사용자 관리 / 로그아웃
// 메뉴바 오른쪽에도 사용자 버튼(내 계정·사용자 관리·로그아웃)을 둔다.
//
//  서버 문서를 편집 중일 때 Ctrl+S 는 서버에 저장한다. 그 문서를 로컬로 저장하면 연결이 끊긴다.
//  창을 닫았다 열어 자동 저장 복구본("X 복구본.hwp")을 되살리면 '서버에 저장' 이 원래 파일에
//  이어서 저장할지 묻는다.
//
// 페이지를 열 때는 서버에 아무 요청도 보내지 않는다 (메뉴·버튼을 누를 때만).
(() => {
  'use strict';
  const AUTH = '/rhwp-auth';
  const API = AUTH + '/api';
  const IDS = {
    open: 'ext:cloud-open', save: 'ext:cloud-save', saveAs: 'ext:cloud-save-as',
    account: 'ext:account', admin: 'ext:account-admin', logout: 'ext:account-logout',
  };

  /** 현재 문서가 서버의 어느 파일인지. 문서가 바뀌면 무효. */
  let bound = null; // { path, etag, generation }
  let services = null; // 마지막으로 받은 CommandServices
  let busy = false;

  const baseName = (p) => p.split('/').pop();
  const dirOf = (p) => p.split('/').slice(0, -1).join('/');

  // ---- 서버 API ---------------------------------------------------------
  class ApiError extends Error {
    constructor(status, message, body) { super(message); this.status = status; this.body = body; }
  }

  async function call(method, url, { body, headers } = {}) {
    let res;
    try {
      res = await fetch(url, { method, body, headers, credentials: 'same-origin', cache: 'no-store' });
    } catch (e) {
      throw new ApiError(0, '서버에 연결할 수 없습니다.');
    }
    if (res.ok) return res;
    const ct = res.headers.get('content-type') || '';
    let data = null;
    if (ct.includes('application/json')) data = await res.json().catch(() => null);
    const msg = (data && data.error && res.status !== 401) ? data.error : {
      401: '로그인이 만료되었습니다.\n이 탭을 새로고침하면 편집 내용이 사라질 수 있으니, 먼저 [파일] > 저장(로컬)으로 보관하거나 새 탭에서 로그인한 뒤 다시 시도하세요.',
      404: '서버 저장소를 찾을 수 없습니다. (로그인 서비스 rhwp-auth 를 쓰는 구성에서만 동작합니다)',
      413: '파일이 너무 커서 서버에 저장할 수 없습니다.',
      502: '서버 저장소(rhwp-auth)가 응답하지 않습니다.',
      503: '서버 저장소를 사용할 수 없습니다.',
    }[res.status] || `서버 오류 (HTTP ${res.status})`;
    throw new ApiError(res.status, msg, data);
  }

  const q = (p) => '?path=' + encodeURIComponent(p);
  const listDir = async (p) => (await call('GET', API + '/fs' + q(p))).json();
  const me = async () => (await call('GET', API + '/me')).json();

  async function putFile(path, bytes, csrf, cond) {
    const headers = { 'X-RHWP-CSRF': csrf, 'Content-Type': 'application/octet-stream' };
    if (cond && cond.ifMatch) headers['If-Match'] = cond.ifMatch;
    if (cond && cond.ifNoneMatch) headers['If-None-Match'] = '*';
    return (await call('PUT', API + '/fs/file' + q(path), { body: bytes, headers })).json();
  }

  // ---- 브라우저에 남기는 작은 기록 (다른 사용자가 로그인하면 지워진다: rhwp.cloud.*) --------
  function lsGet(k, d) { try { const v = localStorage.getItem(k); return v === null ? d : JSON.parse(v); } catch (e) { return d; } }
  function lsSet(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch (e) { /* ignore */ } }
  const MEMO_KEY = 'rhwp.cloud.paths';   // { 경로: etag } — 복구본을 원래 파일에 잇기 위해
  const DIR_KEY = 'rhwp.cloud.lastDir';  // 마지막으로 연 폴더
  function remember(path, etag) {
    const m = lsGet(MEMO_KEY, {}) || {};
    delete m[path];
    if (etag) m[path] = etag;
    const keys = Object.keys(m);
    for (const k of keys.slice(0, Math.max(0, keys.length - 40))) delete m[k];
    lsSet(MEMO_KEY, m);
  }

  // ---- 편집기 연동 ------------------------------------------------------
  // 서버 문서와 연결된 상태인가. 문서가 바뀌었거나, 로컬로 저장·다른 이름으로 저장했으면 끊는다.
  function isBound(svc) {
    if (!bound) return false;
    if (svc.wasm.documentGeneration !== bound.generation
        || svc.wasm.currentFileHandle || svc.wasm.fileName !== baseName(bound.path)) {
      bound = null;
      return false;
    }
    return true;
  }

  /** 자동 저장 복구본("X 복구본.hwp")이면 원래 서버 파일 {path, etag} 를 찾는다 (하나일 때만). */
  function recoveredFrom(svc) {
    const mm = /^(.*) 복구본\.hwp$/.exec(svc.wasm.fileName || '');
    if (!mm || svc.wasm.currentFileHandle) return null;
    const m = lsGet(MEMO_KEY, {}) || {};
    const hits = Object.keys(m).filter((p) => baseName(p).replace(/\.(hwpx?|hml)$/i, '') === mm[1]);
    return hits.length === 1 ? { path: hits[0], etag: m[hits[0]] } : null;
  }

  // 편집기는 연 문서를 '최근 문서'에 넣는데, 서버 문서는 로컬 핸들이 없어 거기서 다시 열 수 없다
  // (누르면 로컬 파일 선택 창이 뜬다). 그래서 서버에서 연 항목은 최근 문서에서 뺀다.
  function dropFromRecent(name) {
    return new Promise((resolve) => {
      let req;
      try { req = indexedDB.open('rhwpStudioRecent'); } catch (e) { return resolve(); }
      req.onupgradeneeded = () => req.transaction.abort();
      req.onerror = req.onblocked = () => resolve();
      req.onsuccess = () => {
        const db = req.result;
        try {
          if (!db.objectStoreNames.contains('recent')) { db.close(); return resolve(); }
          const tx = db.transaction('recent', 'readwrite');
          const st = tx.objectStore('recent');
          const all = st.getAll();
          all.onsuccess = () => {
            for (const r of all.result || []) if (r && r.fileName === name && !r.handle) st.delete(r.id);
          };
          tx.oncomplete = tx.onerror = tx.onabort = () => { db.close(); resolve(); };
        } catch (e) { db.close(); resolve(); }
      };
    });
  }

  function defaultExt(svc) {
    return svc.wasm.getSourceFormat() === 'hwpx' ? '.hwpx' : '.hwp';
  }

  function exportBytes(svc, name) {
    const ih = svc.getInputHandler && svc.getInputHandler();
    if (ih && ih.flushDeferredPaginationIfNeeded) {
      ih.flushDeferredPaginationIfNeeded('save');
      if (ih.hasDeferredPaginationPending && ih.hasDeferredPaginationPending()) {
        throw new Error('쪽 나누기 계산이 끝나지 않았습니다. 잠시 후 다시 시도하세요.');
      }
    }
    return /\.hwpx$/i.test(name) ? svc.wasm.exportHwpx() : svc.wasm.exportHwp();
  }

  function markSaved(svc, info, path, generation) {
    bound = { path, etag: info.etag, generation };
    remember(path, info.etag);
    lsSet(DIR_KEY, dirOf(path));
    try {
      svc.wasm.currentFileHandle = null; // 로컬 '저장' 이 예전 로컬 파일을 덮어쓰지 않게
      svc.wasm.fileName = baseName(path);
      svc.documentState.markClean('save');
      svc.refreshDocumentStatus();
    } catch (e) { console.warn('[cloud] 저장 상태 갱신 실패', e); }
  }

  function guardPassword(svc) {
    if (svc.wasm.requiresPasswordForSave) {
      alert('암호가 설정된 문서는 서버 저장을 지원하지 않습니다.\n[파일] > 저장(로컬)을 사용하세요.');
      return false;
    }
    return true;
  }

  async function saveTo(svc, path, cond, csrf) {
    const generation = svc.wasm.documentGeneration;
    const bytes = exportBytes(svc, path);
    const done = (info) => {
      if (svc.wasm.documentGeneration === generation) markSaved(svc, info, path, generation);
      toast(`서버에 저장했습니다: ${placeLabel(path)}`);
    };
    try {
      done(await putFile(path, bytes, csrf, cond));
      return true;
    } catch (e) {
      if (e.status !== 412) throw e;
      const what = cond.ifNoneMatch
        ? `'${baseName(path)}' 파일이 이미 있습니다. 덮어쓸까요?`
        : `서버의 '${baseName(path)}' 파일이 그 사이 다른 곳에서 바뀌었습니다.\n지금 문서로 덮어쓸까요? (취소하면 저장하지 않습니다)`;
      if (!confirm(what)) return false;
      done(await putFile(path, bytes, csrf, null));
      return true;
    }
  }

  async function cmdSave(svc) {
    if (!guardPassword(svc)) return;
    if (!isBound(svc)) {
      const orig = recoveredFrom(svc);
      if (orig && confirm(`복구한 문서입니다. 원래 서버 파일 '${placeLabel(orig.path)}' 에 저장할까요?\n(취소하면 다른 이름으로 저장합니다)`)) {
        const { csrf } = await me();
        await saveTo(svc, orig.path, { ifMatch: orig.etag }, csrf);
        return;
      }
      return cmdSaveAs(svc);
    }
    const { csrf } = await me();
    await saveTo(svc, bound.path, { ifMatch: bound.etag }, csrf);
  }

  async function cmdSaveAs(svc) {
    if (!guardPassword(svc)) return;
    const startDir = isBound(svc) ? dirOf(bound.path) : (lsGet(DIR_KEY, 'my') || 'my');
    const stem = (isBound(svc) ? baseName(bound.path) : svc.wasm.fileName || '문서').replace(/\.(hwpx?|hml|pdf)$/i, '');
    const choice = await browser({ mode: 'save', start: startDir, stem, ext: defaultExt(svc) });
    if (!choice) return;
    const same = isBound(svc) && bound.path === choice.path;
    await saveTo(svc, choice.path, same ? { ifMatch: bound.etag } : { ifNoneMatch: true }, choice.csrf);
  }

  async function cmdOpen(svc) {
    const picked = await browser({ mode: 'open', start: lsGet(DIR_KEY, 'my') || 'my' });
    if (!picked) return;
    const res = await call('GET', API + '/fs/file' + q(picked.path));
    const etag = res.headers.get('etag') || picked.etag;
    const bytes = new Uint8Array(await res.arrayBuffer());
    const requestId = 'cloud-' + Date.now() + '-' + Math.random().toString(36).slice(2);
    const done = new Promise((resolve) => {
      const off = svc.eventBus.on('open-document-bytes:done', (p) => {
        if (!p || p.requestId !== requestId) return;
        off && off();
        resolve(p);
      });
    });
    // skipUnsavedGuard 를 주지 않는다 → 저장 안 된 변경이 있으면 편집기가 먼저 묻는다
    svc.eventBus.emit('open-document-bytes', { bytes, fileName: picked.name, fileHandle: null, requestId });
    const result = await done;
    if (result.ok) {
      bound = { path: picked.path, etag, generation: svc.wasm.documentGeneration };
      remember(picked.path, etag);
      lsSet(DIR_KEY, dirOf(picked.path));
      setTimeout(() => { void dropFromRecent(picked.name); }, 1500); // 최근 문서 기록은 비동기라 조금 뒤에
    }
  }

  // ---- 계정 -------------------------------------------------------------
  async function cmdAccount() { window.open(AUTH + '/', '_blank', 'noopener'); }
  async function cmdAdmin() {
    let info = null;
    try { info = await me(); } catch (e) { /* 아래에서 안내 */ }
    if (info && !info.admin) { alert('사용자 관리는 관리자만 쓸 수 있습니다.'); return; }
    window.open(AUTH + '/admin', '_blank', 'noopener');
  }
  async function cmdLogout() {
    let info;
    try { info = await me(); } catch (e) {
      if (e.status === 401) { location.href = AUTH + '/login?rd=/rhwp/'; return; }
      throw e;
    }
    // 실제 form POST → 로그아웃 화면(서비스 워커·캐시 정리)으로 이동.
    // 저장 안 한 변경이 있으면 브라우저가 '사이트를 나가시겠습니까?' 를 먼저 묻는다.
    const f = document.createElement('form');
    f.method = 'post';
    f.action = AUTH + '/logout';
    const i = document.createElement('input');
    i.type = 'hidden'; i.name = 'csrf'; i.value = info.csrf;
    f.appendChild(i);
    document.body.appendChild(f);
    f.submit();
  }

  function run(fn) {
    return (svc) => {
      if (svc) services = svc;
      if (busy) { toast('이전 작업을 처리하는 중입니다. 잠시 후 다시 시도하세요.'); return; }
      busy = true;
      Promise.resolve()
        .then(() => fn(svc || services))
        .catch((e) => {
          console.warn('[cloud]', e);
          alert(e instanceof Error ? e.message : String(e));
        })
        .finally(() => { busy = false; });
    };
  }

  // ---- UI ---------------------------------------------------------------
  const CSS = `
.rhwp-cloud-back{position:fixed;inset:0;background:rgba(0,0,0,.4);z-index:100000;display:flex;align-items:center;justify-content:center;padding:16px}
.rhwp-cloud{background:var(--rhwp-cloud-bg,#fff);color:var(--rhwp-cloud-fg,#222);border-radius:8px;box-shadow:0 10px 40px rgba(0,0,0,.3);
  width:min(620px,100%);height:min(640px,100%);display:flex;flex-direction:column;font:14px/1.5 system-ui,-apple-system,"Malgun Gothic",sans-serif}
.rhwp-cloud h2{margin:0;padding:12px 16px;font-size:16px;border-bottom:1px solid var(--rhwp-cloud-line,#ddd);display:flex;justify-content:space-between;gap:8px}
.rhwp-cloud h2 small{font-weight:normal;opacity:.6}
.rhwp-cloud .crumbs{display:flex;flex-wrap:wrap;align-items:center;gap:2px;padding:8px 12px;border-bottom:1px solid var(--rhwp-cloud-line,#eee);font-size:13px}
.rhwp-cloud .crumbs button{border:0;padding:2px 6px;background:transparent;color:inherit;cursor:pointer;border-radius:4px}
.rhwp-cloud .crumbs button:hover{background:var(--rhwp-cloud-hover,#eef4ff)}
.rhwp-cloud .crumbs .sep{opacity:.5}
.rhwp-cloud .crumbs .ro{margin-left:auto;font-size:12px;color:#b45309}
.rhwp-cloud .list{overflow:auto;flex:1;min-height:120px}
.rhwp-cloud .row{display:flex;align-items:center;gap:8px;padding:8px 16px;border-bottom:1px solid var(--rhwp-cloud-line,#eee);cursor:pointer}
.rhwp-cloud .row:hover,.rhwp-cloud .row.sel{background:var(--rhwp-cloud-hover,#eef4ff)}
.rhwp-cloud .row .i{width:1.4em;text-align:center}
.rhwp-cloud .row .n{flex:1;min-width:0;overflow-wrap:anywhere}
.rhwp-cloud .row .m{opacity:.6;font-size:12px;white-space:nowrap}
.rhwp-cloud .row button{padding:2px 8px}
.rhwp-cloud .empty{padding:24px 16px;opacity:.6;text-align:center}
.rhwp-cloud .foot{display:flex;gap:8px;padding:12px 16px;border-top:1px solid var(--rhwp-cloud-line,#ddd);align-items:center;flex-wrap:wrap}
.rhwp-cloud .foot input{flex:1;min-width:140px;padding:6px 8px;font:inherit}
.rhwp-cloud .foot select{padding:6px;font:inherit}
.rhwp-cloud button{font:inherit;padding:6px 14px;border-radius:4px;border:1px solid #999;background:transparent;color:inherit;cursor:pointer}
.rhwp-cloud button.primary{background:#2563eb;border-color:#2563eb;color:#fff}
.rhwp-cloud button:disabled{opacity:.5;cursor:default}
.rhwp-cloud .err{color:#c62828;font-size:13px;width:100%}
@media (max-width:480px){.rhwp-cloud .row{flex-wrap:wrap}.rhwp-cloud .row .n{flex-basis:calc(100% - 2em)}.rhwp-cloud .row .m{flex:1;padding-left:2em}}
@media (prefers-color-scheme: dark){.rhwp-cloud{--rhwp-cloud-bg:#2b2b2b;--rhwp-cloud-fg:#eee;--rhwp-cloud-line:#444;--rhwp-cloud-hover:#33415c}}
.rhwp-cloud-toast{position:fixed;left:50%;bottom:48px;transform:translateX(-50%);background:#333;color:#fff;padding:8px 16px;border-radius:6px;z-index:100001;font:14px system-ui,sans-serif;max-width:90vw}
#menu-bar .rhwp-user-btn{margin-left:auto;flex:0 0 auto;display:flex;align-items:center;gap:4px;height:22px;padding:0 8px;border:0;border-radius:4px;background:transparent;color:inherit;font:inherit;cursor:pointer;white-space:nowrap}
#menu-bar .rhwp-user-btn:hover,#menu-bar .rhwp-user-btn[aria-expanded=true]{background:var(--ui-hover-bg,rgba(0,0,0,.08))}
#menu-bar .rhwp-user-btn + .menu-toolbox-toggle{margin-left:4px}
.rhwp-user-menu{position:fixed;z-index:100000;min-width:180px;background:var(--rhwp-cloud-bg,#fff);color:var(--rhwp-cloud-fg,#222);border:1px solid var(--rhwp-cloud-line,#ccc);border-radius:6px;box-shadow:0 6px 20px rgba(0,0,0,.2);padding:4px 0;font:14px system-ui,-apple-system,"Malgun Gothic",sans-serif}
.rhwp-user-menu .who{padding:6px 14px;opacity:.65;font-size:12px;border-bottom:1px solid var(--rhwp-cloud-line,#eee);margin-bottom:4px}
.rhwp-user-menu button{display:block;width:100%;text-align:left;border:0;background:transparent;color:inherit;font:inherit;padding:8px 14px;cursor:pointer}
.rhwp-user-menu button:hover{background:var(--rhwp-cloud-hover,#eef4ff)}
@media (prefers-color-scheme: dark){.rhwp-user-menu{--rhwp-cloud-bg:#2b2b2b;--rhwp-cloud-fg:#eee;--rhwp-cloud-line:#444;--rhwp-cloud-hover:#33415c}}
`;
  function ensureCss() {
    if (document.getElementById('rhwp-cloud-css')) return;
    const s = document.createElement('style');
    s.id = 'rhwp-cloud-css';
    s.textContent = CSS;
    document.head.appendChild(s);
  }

  function el(tag, props = {}, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(props)) {
      if (k === 'class') e.className = v;
      else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
      else if (v !== undefined) e[k] = v;
    }
    for (const c of kids) if (c != null) e.append(c);
    return e;
  }

  function toast(msg) {
    ensureCss();
    const t = el('div', { class: 'rhwp-cloud-toast', textContent: msg });
    document.body.appendChild(t);
    setTimeout(() => t.remove(), 2500);
  }

  const fmtSize = (n) => n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(0)} KB` : `${(n / 1048576).toFixed(1)} MB`;
  const fmtTime = (s) => { const d = new Date(s); return isNaN(d) ? '' : d.toLocaleString(); };

  /** 사람이 읽는 위치: my/a/b.hwp → 내 문서 › a › b.hwp, mount/공용/x → 공용 › x */
  function placeLabel(p) {
    const parts = p.split('/');
    if (parts[0] === 'my') parts[0] = '내 문서';
    else if (parts[0] === 'mount') parts.shift();
    return parts.join(' › ');
  }

  /** 모달 하나를 띄우고, close(value) 로 닫는다. 편집기 단축키가 가로채지 않게 키 이벤트를 막는다. */
  function modal(build) {
    ensureCss();
    return new Promise((resolve) => {
      const prevFocus = document.activeElement;
      const back = el('div', { class: 'rhwp-cloud-back' });
      const box = el('div', { class: 'rhwp-cloud', role: 'dialog' });
      back.appendChild(box);
      // Esc 는 초점이 어디에 있든 닫는다 (편집기보다 먼저 받는다)
      const onEsc = (e) => { if (e.key === 'Escape') { e.preventDefault(); e.stopImmediatePropagation(); close(null); } };
      window.addEventListener('keydown', onEsc, true);
      const close = (v) => {
        window.removeEventListener('keydown', onEsc, true);
        back.remove();
        try { prevFocus && prevFocus.focus && prevFocus.focus(); } catch (e) { /* ignore */ }
        resolve(v);
      };
      for (const ev of ['keydown', 'keyup', 'keypress', 'input', 'beforeinput', 'compositionstart', 'compositionend', 'paste', 'copy', 'cut']) {
        box.addEventListener(ev, (e) => e.stopPropagation());
      }
      back.addEventListener('mousedown', (e) => { if (e.target === back) close(null); });
      build(box, close);
      document.body.appendChild(back);
    });
  }

  const BAD = /[\\/:*?"<>|\u0000-\u001f]/;

  /**
   * 서버 파일 탐색기. mode=open → {path,name,etag}, mode=save → {path,csrf}.
   * 최상위('서버')에는 '내 문서' 와 공유 폴더가 보인다. 폴더는 눌러서 들어간다.
   */
  function browser({ mode, start, stem, ext }) {
    return modal((box, close) => {
      let data = null;
      let sel = null;
      let pendingSubmit = false; // 목록을 받기 전에 Enter/저장을 누른 경우
      const crumbs = el('div', { class: 'crumbs' });
      const list = el('div', { class: 'list' });
      const err = el('div', { class: 'err' });
      const title = el('span', { textContent: mode === 'open' ? '서버에서 열기' : '서버에 다른 이름으로 저장' });
      const who = el('small');
      const input = el('input', { type: 'text', value: stem || '', placeholder: '파일 이름', spellcheck: false });
      const fmt = el('select', {}, el('option', { value: '.hwp', textContent: '.hwp' }), el('option', { value: '.hwpx', textContent: '.hwpx' }));
      fmt.value = ext || '.hwp';
      const okBtn = el('button', { class: 'primary', textContent: mode === 'open' ? '열기' : '저장', disabled: true });

      async function go(path) {
        err.textContent = '';
        data = null;
        okBtn.disabled = true;
        list.replaceChildren(el('div', { class: 'empty', textContent: '불러오는 중…' }));
        try {
          data = await listDir(path);
        } catch (e) {
          if (e.status === 404 && path !== 'my' && path !== '') { return go('my'); } // 지워진 폴더 → 내 문서
          list.replaceChildren();
          err.textContent = e.message;
          return;
        }
        data.entries = data.entries || [];
        sel = null;
        who.textContent = `☁ ${data.user}`;
        render();
        if (pendingSubmit) { pendingSubmit = false; submit(); }
      }

      function render() {
        const p = data.path;
        // 경로 표시 (누르면 그 폴더로)
        crumbs.replaceChildren(el('button', { textContent: '서버', onclick: () => go('') }));
        const parts = p ? p.split('/') : [];
        let acc = [];
        parts.forEach((seg, i) => {
          acc.push(seg);
          if (seg === 'mount' && i === 0) return;
          const to = acc.join('/');
          crumbs.append(el('span', { class: 'sep', textContent: '›' }),
            el('button', { textContent: i === 0 && seg === 'my' ? '내 문서' : seg, onclick: () => go(to) }));
        });
        const canWrite = !!data.writable && p !== '' && p !== 'mount';
        if (mode === 'save' && p && !canWrite) crumbs.append(el('span', { class: 'ro', textContent: '여기에는 저장할 수 없음' }));
        okBtn.disabled = mode === 'open' ? !sel : !canWrite;

        list.replaceChildren();
        if (p) {
          list.append(el('div', { class: 'row', onclick: () => go(parts.length <= 2 && parts[0] === 'mount' ? '' : parts.slice(0, -1).join('/')) },
            el('span', { class: 'i', textContent: '⬆' }), el('span', { class: 'n', textContent: '상위 폴더' })));
        }
        if (!data.entries.length) list.append(el('div', { class: 'empty', textContent: p === '' ? '' : '이 폴더에는 문서가 없습니다.' }));
        for (const f of data.entries) {
          if (f.type !== 'file') {
            const label = f.type === 'space' ? (f.path === 'my' ? '내 문서' : `${f.name} (공유)`) : f.name;
            list.append(el('div', { class: 'row', onclick: () => go(f.path) },
              el('span', { class: 'i', textContent: f.type === 'space' ? (f.path === 'my' ? '🏠' : '🗂') : '📁' }),
              el('span', { class: 'n', textContent: label }),
              el('span', { class: 'm', textContent: f.type === 'space' && f.path !== 'my' && !f.writable ? '읽기 전용' : '' })));
            continue;
          }
          const canDelete = p.startsWith('my') || data.admin;
          const del = canDelete ? el('button', {
            textContent: '삭제', title: '서버에서 삭제',
            onclick: async (e) => {
              e.stopPropagation();
              if (!confirm(`'${f.name}' 을(를) 서버에서 삭제할까요? 되돌릴 수 없습니다.`)) return;
              try {
                await call('DELETE', API + '/fs/file' + q(f.path), { headers: { 'X-RHWP-CSRF': data.csrf, 'If-Match': f.etag } });
                if (bound && bound.path === f.path) bound = null;
                remember(f.path, null);
                go(p);
              } catch (ex) { err.textContent = ex.message; }
            },
          }) : null;
          const row = el('div', {
            class: 'row' + (sel === f ? ' sel' : ''),
            onclick: () => {
              if (mode === 'save') {
                const m = f.name.match(/^(.*)(\.hwpx?|\.hml)$/i);
                input.value = m ? m[1] : f.name;
                if (m && /^\.hwpx?$/i.test(m[2])) fmt.value = m[2].toLowerCase();
                input.focus();
                return;
              }
              sel = f; render();
            },
            ondblclick: () => { if (mode === 'open') close(f); },
          }, el('span', { class: 'i', textContent: '📄' }), el('span', { class: 'n', textContent: f.name }),
          el('span', { class: 'm', textContent: `${fmtSize(f.size)} · ${fmtTime(f.mtime)}` }), del);
          list.append(row);
        }
      }

      function submit() {
        if (!data) { pendingSubmit = mode === 'save'; return; }
        if (mode === 'open') { if (sel) close(sel); return; }
        if (okBtn.disabled) return;
        const s = input.value.trim().replace(/\.(hwpx?|hml)$/i, '');
        const name = s + fmt.value;
        if (!s) { err.textContent = '파일 이름을 입력하세요.'; return; }
        if (BAD.test(s) || s.startsWith('.') || s.startsWith('@') || s.startsWith('#')) {
          err.textContent = '\\ / : * ? " < > | 와 . @ # 로 시작하는 이름은 쓸 수 없습니다.'; return;
        }
        if (new TextEncoder().encode(name).length > 200) { err.textContent = '파일 이름이 너무 깁니다.'; return; }
        close({ path: data.path + '/' + name, csrf: data.csrf });
      }
      okBtn.addEventListener('click', submit);
      input.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.isComposing) { e.preventDefault(); submit(); } });
      box.addEventListener('keydown', (e) => { if (mode === 'open' && e.key === 'Enter' && sel) { e.preventDefault(); close(sel); } });

      const foot = el('div', { class: 'foot' });
      if (mode === 'save') foot.append(input, fmt);
      foot.append(err, el('span', { style: 'flex:1' }), el('button', { textContent: '취소', onclick: () => close(null) }), okBtn);
      box.append(el('h2', {}, title, who), crumbs, list, foot);
      if (mode === 'save') setTimeout(() => { input.focus(); input.select(); }, 0);
      go(start || 'my');
    });
  }

  // 메뉴바 오른쪽 사용자 버튼
  function addUserButton() {
    const bar = document.getElementById('menu-bar');
    if (!bar || bar.querySelector('.rhwp-user-btn')) return;
    ensureCss();
    const btn = el('button', { type: 'button', class: 'rhwp-user-btn', title: '계정', textContent: '👤 계정 ▾' });
    btn.setAttribute('aria-haspopup', 'menu');
    btn.setAttribute('aria-expanded', 'false');
    let pop = null;
    const closePop = () => { if (pop) { pop.remove(); pop = null; btn.setAttribute('aria-expanded', 'false'); } };
    btn.addEventListener('click', async (e) => {
      e.stopPropagation();
      if (pop) return closePop();
      const r = btn.getBoundingClientRect();
      const whoLine = el('div', { class: 'who', textContent: '…' });
      const adminBtn = el('button', { textContent: '사용자 관리', onclick: () => { closePop(); run(cmdAdmin)(); } });
      pop = el('div', { class: 'rhwp-user-menu', role: 'menu' }, whoLine,
        el('button', { textContent: '내 계정 · 비밀번호 변경', onclick: () => { closePop(); run(cmdAccount)(); } }),
        adminBtn,
        el('button', { textContent: '로그아웃', onclick: () => { closePop(); run(cmdLogout)(); } }));
      pop.style.top = `${r.bottom + 4}px`;
      pop.style.right = `${Math.max(8, window.innerWidth - r.right)}px`;
      document.body.appendChild(pop);
      btn.setAttribute('aria-expanded', 'true');
      try {
        const info = await me();
        whoLine.textContent = `${info.user}${info.admin ? ' (관리자)' : ''}`;
        btn.firstChild.textContent = `👤 ${info.user} ▾`;
        if (!info.admin) adminBtn.remove();
      } catch (ex) { whoLine.textContent = ex.status === 401 ? '로그인 필요' : '계정 정보를 불러오지 못함'; }
    });
    document.addEventListener('mousedown', (e) => { if (pop && !pop.contains(e.target) && e.target !== btn) closePop(); });
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closePop(); });
    const toggle = bar.querySelector('#toolbox-basic-toggle');
    bar.insertBefore(btn, toggle || null);
  }

  // ---- 등록 -------------------------------------------------------------
  function register(auto) {
    const defs = [
      { id: IDS.open, label: '☁ 서버에서 열기...', opensDialog: true, execute: run(cmdOpen) },
      { id: IDS.save, label: '☁ 서버에 저장', opensDialog: true, execute: run(cmdSave) },
      { id: IDS.saveAs, label: '☁ 서버에 다른 이름으로 저장...', opensDialog: true, execute: run(cmdSaveAs) },
      { id: IDS.account, label: '👤 내 계정 · 비밀번호 변경', execute: run(cmdAccount) },
      { id: IDS.admin, label: '👤 사용자 관리 (관리자)', execute: run(cmdAdmin) },
      { id: IDS.logout, label: '👤 로그아웃', execute: run(cmdLogout) },
    ];
    for (const d of defs) auto.registerCommand(d);
    for (const d of defs) {
      try { auto.addMenuItem({ menuId: 'file', commandId: d.id, position: 'bottom' }); } catch (e) {
        console.warn('[cloud] 메뉴 추가 실패', e);
      }
    }
    try { addUserButton(); } catch (e) { console.warn('[cloud] 사용자 버튼 추가 실패', e); }
    // 서버 문서를 편집 중이면 Ctrl+S → 서버에 저장 (편집기 단축키보다 먼저 받는다)
    window.addEventListener('keydown', (e) => {
      if (!(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey || e.key.toLowerCase() !== 's') return;
      if (!services || !isBound(services) || document.querySelector('.rhwp-cloud-back')) return;
      e.preventDefault();
      e.stopImmediatePropagation();
      auto.execute(IDS.save, undefined, { allowDialog: true });
    }, true);
  }

  // 편집기(main 모듈)가 확장 API 를 준비할 때까지 기다린다
  let tries = 0;
  (function wait() {
    const auto = window.rhwpStudio && window.rhwpStudio.automation;
    if (auto) {
      try { register(auto); } catch (e) { console.warn('[cloud] 등록 실패', e); }
      return;
    }
    if (++tries > 600) { console.warn('[cloud] rhwpStudio.automation 을 찾지 못해 서버 저장 메뉴를 붙이지 못했습니다'); return; }
    setTimeout(wait, 100);
  })();
})();
