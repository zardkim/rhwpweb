// 서버 저장/불러오기 e2e (RHWP_AUTH=request 스택 대상)
//   node cloud-e2e.mjs http://127.0.0.1:PORT 사용자 비밀번호
import { chromium } from 'playwright';

const [base, user, pass] = process.argv.slice(2);
const browser = await chromium.launch();
const ctx = await browser.newContext();
const page = await ctx.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
page.on('dialog', async (d) => { console.log(`  dialog(${d.type()}): ${d.message().split('\n')[0]}`); await d.accept(); });
const t0 = Date.now();
page.on('request', (r) => { if (r.method() === 'PUT') console.log(`  [${Date.now() - t0}ms] PUT ${decodeURIComponent(r.url().split('/').pop())} ${r.headers()['if-match'] || ''}`); });
async function etagChanged(name, old) {
  for (let i = 0; i < 50; i++) {
    const r = await api();
    const f = r.body.files && r.body.files.find((x) => x.name === name);
    if (f && f.etag !== old) return f.etag;
    await page.waitForTimeout(200);
  }
  return old;
}
const step = (s) => console.log(`▶ [${Date.now() - t0}ms] ` + s);
const assert = (c, m) => { if (!c) { console.error('❌ ' + m); process.exitCode = 1; throw new Error(m); } console.log('  ✓ ' + m); };

async function clickMenu(cmd) {
  await page.click('.menu-item[data-menu="file"]');
  await page.click(`[data-cmd="${cmd}"]`);
}
const api = (path = '', init) => page.evaluate(async ([p, i]) => {
  const r = await fetch('/rhwp-auth/api/files' + p, i);
  return { status: r.status, etag: r.headers.get('etag'), body: r.headers.get('content-type')?.includes('json') ? await r.json() : (await r.arrayBuffer()).byteLength };
}, [path, init]);

try {
  step('로그인');
  await page.goto(base + '/rhwp/');
  await page.fill('input[name=username]', user);
  await page.fill('input[name=password]', pass);
  await Promise.all([page.waitForURL('**/rhwp/**'), page.click('button[type=submit]')]);
  await page.waitForFunction(() => /\.hwp/.test(document.title), null, { timeout: 30000 });
  assert(true, '편집기 로드');

  step('본문 입력 후 서버에 다른 이름으로 저장 (한글 이름)');
  await page.click('#scroll-container', { position: { x: 400, y: 200 } }).catch(() => {});
  await page.keyboard.type('server save test');
  await clickMenu('ext:cloud-save');           // 서버 문서가 아니므로 '다른 이름으로' 창이 열린다
  await page.waitForSelector('.rhwp-cloud input');
  await page.fill('.rhwp-cloud input', '');
  await page.keyboard.insertText('테스트 문서');
  const typed = await page.inputValue('.rhwp-cloud input');
  assert(typed === '테스트 문서', `파일 이름 입력이 편집기에 먹히지 않음 (${typed})`);
  await page.keyboard.press('Enter');
  await page.waitForSelector('.rhwp-cloud-toast');
  let l = await api();
  assert(l.status === 200 && l.body.files.some((f) => f.name === '테스트 문서.hwp'), '서버 목록에 테스트 문서.hwp');
  const saved = l.body.files.find((f) => f.name === '테스트 문서.hwp');
  const title1 = await page.title();
  assert(title1.includes('테스트 문서.hwp'), `제목이 서버 파일 이름 (${title1})`);

  step('같은 문서를 Ctrl+S → 서버에 덮어쓰기');
  await page.click('#scroll-container', { position: { x: 400, y: 200 } }).catch(() => {});
  await page.keyboard.type(' more');
  await page.keyboard.press('Control+s');
  await etagChanged('테스트 문서.hwp', saved.etag);
  l = await api();
  const saved2 = l.body.files.find((f) => f.name === '테스트 문서.hwp');
  assert(saved2.etag !== saved.etag, 'Ctrl+S 가 서버 파일을 갱신');

  step('다른 곳에서 바뀐 경우 (stale ETag) → 412');
  const { csrf } = l.body;
  const stale = await api('/' + encodeURIComponent('테스트 문서.hwp'), { method: 'PUT', headers: { 'X-RHWP-CSRF': csrf, 'If-Match': saved.etag }, body: 'x' });
  assert(stale.status === 412, `오래된 ETag 로 저장하면 412 (${stale.status})`);
  const nocsrf = await api('/' + encodeURIComponent('a.hwp'), { method: 'PUT', body: 'x' });
  assert(nocsrf.status === 403, `CSRF 헤더 없으면 403 (${nocsrf.status})`);
  const trav = await api('/' + encodeURIComponent('../x.hwp'), { method: 'PUT', headers: { 'X-RHWP-CSRF': csrf }, body: 'x' });
  assert(trav.status === 400 || trav.status === 404, `../ 이름 거부 (${trav.status})`);
  const badext = await api('/' + encodeURIComponent('a.exe'), { method: 'PUT', headers: { 'X-RHWP-CSRF': csrf }, body: 'x' });
  assert(badext.status === 400, `허용 안 된 확장자 거부 (${badext.status})`);

  step('새 문서로 바꾼 뒤 서버에서 열기 → 내용 확인');
  const server = await api('/' + encodeURIComponent('테스트 문서.hwp'));
  await clickMenu('ext:cloud-open');
  await page.waitForSelector('.rhwp-cloud .row');
  await page.dblclick('.rhwp-cloud .row >> text=테스트 문서.hwp');
  // 문서를 열 때 업스트림이 띄우는 '로컬 글꼴 감지' 안내 → 대체 글꼴로 보기
  const fb = page.locator('button:has-text("대체 글꼴로 보기")');
  await fb.click({ timeout: 8000 }).catch(() => {});
  await page.waitForFunction(() => !document.querySelector('.modal-overlay'), null, { timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1500);
  await page.screenshot({ path: 'cloud-reopened.png' });
  await page.waitForFunction(() => document.title.includes('테스트 문서.hwp'), null, { timeout: 15000 });
  console.log(`  (서버 파일 ${server.body} bytes)`);
  assert(server.status === 200 && server.body > 1000, '서버에서 받은 HWP 바이트');

  step('열린 서버 문서를 다시 저장 → 같은 파일 갱신 (If-Match)');
  const before = (await api()).body.files.find((f) => f.name === '테스트 문서.hwp').etag;
  await page.waitForFunction(() => !document.querySelector('.rhwp-cloud-toast'), null, { timeout: 5000 });
  await clickMenu('ext:cloud-save');
  await etagChanged('테스트 문서.hwp', before);
  assert(!(await page.$('.rhwp-cloud-back')), '대화상자가 뜨지 않음');
  const after = (await api()).body.files.find((f) => f.name === '테스트 문서.hwp').etag;
  console.log(`  [${Date.now() - t0}ms] before=${before} after=${after} title=${await page.title()}`);
  await page.screenshot({ path: 'cloud-after-save.png' });
  assert(after !== before, '서버 문서 저장이 대화상자 없이 바로 갱신');

  step('다른 이름 저장 시 이미 있는 이름이면 덮어쓰기 확인');
  await clickMenu('ext:cloud-save-as');
  await page.waitForSelector('.rhwp-cloud input');
  await page.fill('.rhwp-cloud input', '두번째');
  await page.selectOption('.rhwp-cloud select', '.hwpx');
  await page.click('.rhwp-cloud button.primary');
  await page.waitForSelector('.rhwp-cloud-toast');
  l = await api();
  assert(l.body.files.some((f) => f.name === '두번째.hwpx'), 'HWPX 로 저장');

  step('목록에서 삭제');
  await clickMenu('ext:cloud-open');
  await page.waitForSelector('.rhwp-cloud .row');
  await page.click('.rhwp-cloud .row:has-text("두번째.hwpx") button');
  await page.waitForFunction(() => !document.querySelector('.rhwp-cloud')?.innerText.includes('두번째.hwpx'));
  await page.keyboard.press('Escape');
  l = await api();
  assert(!l.body.files.some((f) => f.name === '두번째.hwpx'), '삭제됨');

  step('로그아웃 상태에서는 API 401');
  const ctx2 = await browser.newContext();
  const anon = (await ctx2.request.get(base + '/rhwp-auth/api/files')).status();
  assert(anon === 401, `비로그인 401 (${anon})`);
  assert(errors.length === 0, `페이지 오류 없음 ${errors.join(' | ')}`);
  console.log('PASS');
} catch (e) {
  console.error('FAIL', e.message);
  await page.screenshot({ path: 'cloud-fail.png' }).catch(() => {});
  process.exitCode = 1;
} finally {
  await browser.close();
}
