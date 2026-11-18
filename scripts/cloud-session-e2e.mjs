// 서버 저장과 자동 저장·복구·로컬 저장·사용자 전환의 연동 e2e
//   node cloud-session-e2e.mjs http://127.0.0.1:PORT 사용자1 비번1 사용자2 비번2
// 확인 항목
//   1) 서버 문서 편집 → 자동 저장 복구본 생김 → 서버 저장하면 복구본 지워짐
//   2) 저장 안 한 채 창을 닫고 다시 열면 복구 → '서버에 저장' 이 원래 서버 파일에 이어서 저장
//   3) 서버 문서를 로컬 '다른 이름으로 저장' 하면 서버 연결이 끊겨 Ctrl+S 가 서버로 가지 않음
//   4) 다른 사용자가 같은 브라우저로 로그인하면 앞 사람의 복구본이 보이지 않음
import { chromium } from 'playwright';

const [base, u1, p1, u2, p2] = process.argv.slice(2);
const browser = await chromium.launch();
const ctx = await browser.newContext({ acceptDownloads: true });
const page = await ctx.newPage();
const dialogs = [];
page.on('dialog', async (d) => { dialogs.push(d.message()); console.log(`  dialog: ${d.message().split('\n')[0]}`); await d.accept(); });
const puts = [];
page.on('request', (r) => { if (r.method() === 'PUT') puts.push(decodeURIComponent(r.url().split('/').pop())); });
let fail = 0;

const ok = (c, m) => { console.log((c ? '  ✓ ' : '  ❌ ') + m); if (!c) fail++; };
const step = (s) => console.log('▶ ' + s);

const drafts = () => page.evaluate(() => new Promise((res) => {
  const r = indexedDB.open('rhwpStudioAutosave');
  r.onupgradeneeded = () => r.transaction.abort();
  r.onerror = () => res([]);
  r.onsuccess = () => {
    const db = r.result;
    if (!db.objectStoreNames.contains('drafts')) { db.close(); return res([]); }
    const q = db.transaction('drafts').objectStore('drafts').getAll();
    q.onsuccess = () => { db.close(); res(q.result.map((d) => d.fileName)); };
  };
}));
async function login(u, p) {
  await page.goto(base + '/rhwp/');
  await page.fill('input[name=username]', u);
  await page.fill('input[name=password]', p);
  await page.click('button[type=submit]');
  await page.waitForURL('**/rhwp/**');
}
const appReady = () => page.waitForFunction(() => window.rhwpStudio?.automation && /\.hwp/.test(document.title), null, { timeout: 30000 });
const menu = async (cmd) => { await page.click('.menu-item[data-menu="file"]'); await page.click(`[data-cmd="${cmd}"]`); };
const typeInDoc = async (t) => { await page.click('#scroll-container', { position: { x: 400, y: 200 } }); await page.keyboard.type(t); };
const serverEtag = (name) => page.evaluate((n) => fetch('/rhwp-auth/api/files').then((r) => r.json()).then((j) => j.files.find((f) => f.name === n)?.etag), name);
async function waitFor(fn, ms = 15000) {
  const end = Date.now() + ms;
  while (Date.now() < end) { if (await fn()) return true; await page.waitForTimeout(300); }
  return false;
}

try {
  step(`1) ${u1}: 서버 문서 편집 → 자동 저장 복구본 → 서버 저장하면 지워짐`);
  await login(u1, p1); await appReady();
  await menu('ext:cloud-save-as');
  await page.fill('.rhwp-cloud input', '연동 시험');
  await page.keyboard.press('Enter');
  ok(await waitFor(async () => !!(await serverEtag('연동 시험.hwp'))), '서버에 연동 시험.hwp 저장');
  await typeInDoc('first edit ');
  ok(await waitFor(async () => (await drafts()).length > 0, 20000), `편집 후 자동 저장 복구본 생김 ${JSON.stringify(await drafts())}`);
  const e1 = await serverEtag('연동 시험.hwp');
  await page.keyboard.press('Control+s');
  ok(await waitFor(async () => (await serverEtag('연동 시험.hwp')) !== e1), 'Ctrl+S → 서버 저장');
  ok(await waitFor(async () => (await drafts()).length === 0), '서버 저장 후 복구본 지워짐');

  step('2) 저장 안 한 채 창 닫기 → 다시 열어 복구 → 원래 서버 파일에 이어서 저장');
  await menu('ext:cloud-open');
  await page.dblclick('.rhwp-cloud .row >> text=연동 시험.hwp');
  await page.locator('button:has-text("대체 글꼴로 보기")').click({ timeout: 8000 }).catch(() => {});
  await page.waitForFunction(() => document.title.startsWith('연동 시험.hwp') && !document.querySelector('.modal-overlay'), null, { timeout: 15000 });
  await page.waitForTimeout(2500);
  const recent = await page.evaluate(() => new Promise((res) => {
    const r = indexedDB.open('rhwpStudioRecent');
    r.onupgradeneeded = () => r.transaction.abort();
    r.onerror = () => res([]);
    r.onsuccess = () => { const db = r.result; if (!db.objectStoreNames.contains('recent')) { db.close(); return res([]); }
      const q = db.transaction('recent').objectStore('recent').getAll(); q.onsuccess = () => { db.close(); res(q.result.map((x) => x.fileName)); }; };
  }));
  ok(!recent.includes('연동 시험.hwp'), `서버 문서는 '최근 문서'(로컬 전용)에 남지 않음 ${JSON.stringify(recent)}`);
  // 문서 초기화가 끝난 뒤 입력한다 (초기화 완료 시 편집기가 '변경 없음'으로 되돌린다)
  await page.waitForFunction(() => /페이지/.test(document.getElementById('sb-message')?.textContent || ''), null, { timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1000);
  await typeInDoc('unsaved edit ');
  ok(await waitFor(async () => (await drafts()).length > 0, 40000), `복구본 생김 ${JSON.stringify(await drafts())}`);
  await page.goto('about:blank');                        // 창 닫기
  await page.goto(base + '/rhwp/');                      // 다시 열기
  const rec = page.locator('button:has-text("복구")').first();
  ok(await rec.waitFor({ timeout: 20000 }).then(() => true).catch(() => false), '다시 열면 문서 복구 창이 뜸');
  await rec.click();
  await page.locator('button:has-text("대체 글꼴로 보기")').click({ timeout: 8000 }).catch(() => {});
  await page.waitForFunction(() => document.title.includes('복구본'), null, { timeout: 15000 });
  ok(true, `복구됨: ${await page.title()}`);
  const e2 = await serverEtag('연동 시험.hwp');
  dialogs.length = 0;
  await menu('ext:cloud-save');
  ok(await waitFor(async () => (await serverEtag('연동 시험.hwp')) !== e2), '복구본을 원래 서버 파일(연동 시험.hwp)에 저장');
  ok(dialogs.some((m) => m.includes('원래 서버 파일')), '저장 전에 원래 파일에 저장할지 물음');
  ok(!(await page.evaluate(() => fetch('/rhwp-auth/api/files').then((r) => r.json()))).files.some((f) => f.name.includes('복구본')),
    '"복구본" 이라는 새 파일을 만들지 않음');
  ok((await page.title()).startsWith('연동 시험.hwp'), `제목이 원래 이름으로 (${await page.title()})`);

  step('3) 서버 문서를 로컬 "다른 이름으로 저장" → Ctrl+S 가 서버로 가지 않음');
  // 헤드리스에는 저장 창이 없으므로 다운로드 경로(모바일·파이어폭스와 같은 경로)로 시험
  await page.evaluate(() => { delete window.showSaveFilePicker; window.showSaveFilePicker = undefined; });
  const dl = page.waitForEvent('download', { timeout: 15000 }).catch(() => null);
  await menu('file:save-as');
  const nameInput = page.locator('.modal-overlay input[type=text], .dialog input[type=text]').first();
  await nameInput.fill('로컬 사본', { timeout: 8000 }).catch(() => {});
  // 업스트림은 이 경로에서 이름 창을 두 번 띄운다 (저장 옵션 → 내려받을 이름)
  for (let i = 0; i < 3; i++) {
    const b = page.locator('.modal-overlay button:has-text("확인")').first();
    if (!(await b.isVisible().catch(() => false))) break;
    await b.click();
    await page.waitForTimeout(500);
  }
  const d = await dl;
  ok(!!d, `로컬로 내려받음 (${d ? d.suggestedFilename() : '-'})`);
  await page.waitForTimeout(500);
  const before = puts.length;
  await typeInDoc('local only ');
  await page.keyboard.press('Control+s');
  await page.waitForTimeout(2500);
  ok(puts.length === before, `로컬 저장 뒤 Ctrl+S 는 서버로 PUT 하지 않음 (PUT ${puts.length - before}건)`);
  for (let i = 0; i < 3; i++) {   // Ctrl+S 가 연 로컬 저장 창은 닫는다
    const c = page.locator('.modal-overlay button:has-text("취소")').first();
    if (!(await c.isVisible().catch(() => false))) break;
    await c.click(); await page.waitForTimeout(300);
  }
  // 저장하지 않은 편집을 남긴 채 나간다 → 4) 에서 다른 사용자에게 보이면 안 된다
  await typeInDoc('admin secret ');
  ok(await waitFor(async () => (await drafts()).length > 0, 40000), `${u1} 의 저장 안 한 복구본이 있음 ${JSON.stringify(await drafts())}`);

  step(`4) 로그아웃 → ${u2} 로그인: 앞 사람의 복구본·최근 문서가 보이지 않음`);
  await page.goto(base + '/rhwp-auth/');
  await page.click('form[action$="logout"] button');
  await login(u2, p2);
  await page.waitForTimeout(4000);
  const leftover = await drafts();
  ok(leftover.length === 0, `${u2} 브라우저에 앞 사람 복구본 없음 ${JSON.stringify(leftover)}`);
  ok(!(await page.locator('text=문서 복구').isVisible().catch(() => false)), '문서 복구 창이 뜨지 않음');

  step(`5) ${u2} 가 자기 복구본을 남긴 뒤 같은 사용자로 다시 로그인하면 유지`);
  await appReady();
  await typeInDoc('bob draft ');
  ok(await waitFor(async () => (await drafts()).length > 0, 20000), `${u2} 복구본 생김`);
  await page.goto(base + '/rhwp-auth/');
  await page.click('form[action$="logout"] button');
  await login(u2, p2);
  await page.waitForTimeout(2000);
  ok((await drafts()).length > 0, '같은 사용자면 복구본을 지우지 않음');
} catch (e) {
  console.error('FAIL', e.message.split('\n')[0]);
  await page.screenshot({ path: 'session-fail.png' }).catch(() => {});
  fail++;
} finally {
  await browser.close();
}
console.log(fail ? `FAIL (${fail})` : 'PASS');
process.exitCode = fail ? 1 : 0;
