// 공유 폴더(files/mount/*)·폴더 탐색·계정 메뉴 e2e
//   node cloud-mount-e2e.mjs http://127.0.0.1:PORT 관리자 비번 사용자 비번 <쓰기공유> <읽기전용공유>
// 준비: 쓰기 공유 안에 '양식/신청서.hwp', 읽기 전용 공유 안에 '규정.hwp' 가 있어야 한다.
import { chromium } from 'playwright';

const [base, au, ap, uu, up, rw, ro] = process.argv.slice(2);
const browser = await chromium.launch();
let fail = 0;
const ok = (c, m) => { console.log((c ? '  ✓ ' : '  ❌ ') + m); if (!c) fail++; };
const step = (s) => console.log('▶ ' + s);

async function session(u, p) {
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  page.on('dialog', (d) => d.accept());
  await page.goto(base + '/rhwp/');
  await page.fill('input[name=username]', u);
  await page.fill('input[name=password]', p);
  await page.click('button[type=submit]');
  await page.waitForURL('**/rhwp/**');
  await page.waitForFunction(() => window.rhwpStudio?.automation && /\.hwp/.test(document.title), null, { timeout: 30000 });
  return page;
}
const menu = async (page, cmd) => { await page.click('.menu-item[data-menu="file"]'); await page.click(`[data-cmd="${cmd}"]`); };
const rows = (page) => page.$$eval('.rhwp-cloud .row .n', (els) => els.map((e) => e.textContent));
const api = (page, url, init) => page.evaluate(async ([u, i]) => {
  const r = await fetch(u, i); const ct = r.headers.get('content-type') || '';
  return { status: r.status, body: ct.includes('json') ? await r.json() : null };
}, [url, init]);

let page;
try {
  step(`${uu}: 서버에서 열기 → 최상위에 내 문서 + 공유 폴더`);
  page = await session(uu, up);
  await menu(page, 'ext:cloud-open');
  await page.waitForSelector('.rhwp-cloud .crumbs button');
  await page.click('.rhwp-cloud .crumbs button:has-text("서버")');
  await page.waitForFunction(() => document.querySelector('.rhwp-cloud .list')?.innerText.includes('내 문서'));
  const top = await rows(page);
  ok(top.includes('내 문서') && top.includes(`${rw} (공유)`) && top.includes(`${ro} (공유)`), `최상위: ${JSON.stringify(top)}`);

  step('공유 폴더 → 하위 폴더 → 문서 열기');
  await page.click(`.rhwp-cloud .row:has-text("${rw} (공유)")`);
  await page.click('.rhwp-cloud .row:has-text("양식")');
  await page.waitForSelector('.rhwp-cloud .row:has-text("신청서.hwp")');
  ok(!(await page.$('.rhwp-cloud .row:has-text("신청서.hwp") button')), '일반 사용자는 공유 폴더 파일 삭제 버튼 없음');
  await page.dblclick('.rhwp-cloud .row:has-text("신청서.hwp")');
  await page.locator('button:has-text("대체 글꼴로 보기")').click({ timeout: 8000 }).catch(() => {});
  await page.waitForFunction(() => document.title.startsWith('신청서.hwp'), null, { timeout: 15000 });
  await page.waitForTimeout(1500); // 열기 마무리(초기화)가 끝날 때까지
  ok(true, '공유 폴더 문서 열림');

  step('공유 폴더 안 하위 폴더에 다른 이름으로 저장');
  await menu(page, 'ext:cloud-save-as');
  await page.waitForSelector('.rhwp-cloud .crumbs button:has-text("양식")', { timeout: 10000 }).catch(() => {});
  const crumb = await page.$eval('.rhwp-cloud .crumbs', (e) => e.innerText);
  ok(crumb.includes(rw) && crumb.includes('양식'), `저장 창이 연 문서의 폴더에서 시작 (${crumb.replace(/\n/g, ' ')})`);
  await page.fill('.rhwp-cloud input', '작성본');
  await page.click('.rhwp-cloud button.primary');
  await page.waitForSelector('.rhwp-cloud-toast');
  const l = await api(page, '/rhwp-auth/api/fs?path=' + encodeURIComponent(`mount/${rw}/양식`));
  ok(l.body.entries.some((e) => e.name === '작성본.hwp'), '공유 폴더에 저장됨');

  step('읽기 전용 공유 폴더: 저장 버튼 꺼짐, API 는 403');
  await menu(page, 'ext:cloud-save-as');
  await page.waitForSelector('.rhwp-cloud .crumbs');
  await page.click('.rhwp-cloud .crumbs button:has-text("서버")');
  await page.click(`.rhwp-cloud .row:has-text("${ro} (공유)")`);
  await page.waitForSelector('.rhwp-cloud .row:has-text("규정.hwp")');
  ok(await page.isDisabled('.rhwp-cloud button.primary'), '저장 버튼 비활성');
  ok((await page.$eval('.rhwp-cloud .crumbs', (e) => e.innerText)).includes('저장할 수 없음'), '"여기에는 저장할 수 없음" 표시');
  ok(!(await rows(page)).includes('메모.txt'), '문서가 아닌 파일은 숨김');
  await page.keyboard.press('Escape');
  ok(!(await page.$('.rhwp-cloud-back')), 'Esc 로 창이 닫힘');
  const me = await api(page, '/rhwp-auth/api/me');
  const roPut = await api(page, '/rhwp-auth/api/fs/file?path=' + encodeURIComponent(`mount/${ro}/x.hwp`),
    { method: 'PUT', headers: { 'X-RHWP-CSRF': me.body.csrf }, body: 'x' });
  ok(roPut.status === 403 && /읽기 전용/.test(roPut.body?.error || ''), `읽기 전용 → 403 ${JSON.stringify(roPut.body)}`);
  const top2 = await api(page, '/rhwp-auth/api/fs/file?path=' + encodeURIComponent('mount/x.hwp'),
    { method: 'PUT', headers: { 'X-RHWP-CSRF': me.body.csrf }, body: 'x' });
  ok(top2.status === 400, `files/mount 바로 아래는 저장 불가 (${top2.status})`);
  const del = await api(page, '/rhwp-auth/api/fs/file?path=' + encodeURIComponent(`mount/${rw}/양식/작성본.hwp`),
    { method: 'DELETE', headers: { 'X-RHWP-CSRF': me.body.csrf } });
  ok(del.status === 403, `일반 사용자는 공유 폴더 삭제 불가 (${del.status})`);
  const esc = await api(page, '/rhwp-auth/api/fs?path=' + encodeURIComponent('my/../../db'));
  ok(esc.status === 400, `../ 경로 거부 (${esc.status})`);
  const other = await api(page, '/rhwp-auth/api/fs?path=' + encodeURIComponent(`my/../${au}`));
  ok(other.status === 400, `다른 사용자 폴더 접근 거부 (${other.status})`);

  step('계정 버튼: 일반 사용자에게는 사용자 관리 없음, 로그아웃');
  await page.click('#menu-bar .rhwp-user-btn');
  await page.waitForFunction((u) => document.querySelector('.rhwp-user-menu .who')?.textContent === u, uu, { timeout: 5000 });
  const items = await page.$$eval('.rhwp-user-menu button', (b) => b.map((x) => x.textContent));
  ok(items.includes('로그아웃') && !items.includes('사용자 관리'), `메뉴: ${JSON.stringify(items)}`);
  ok((await page.textContent('#menu-bar .rhwp-user-btn')).includes(uu), '버튼에 사용자 이름');
  await page.click('.rhwp-user-menu button:has-text("로그아웃")');
  await page.waitForURL('**/rhwp-auth/logout', { timeout: 10000 }).catch(() => {});
  await page.waitForSelector('text=로그아웃되었습니다', { timeout: 10000 });
  ok(true, '로그아웃 화면');
  await page.goto(base + '/rhwp/');
  ok(page.url().includes('/rhwp-auth/login'), `로그아웃 뒤 편집기 접근 → 로그인 화면 (${page.url()})`);

  step(`${au}: 계정 버튼에 사용자 관리, 새 탭으로 열림 / 공유 폴더 삭제 가능 / 저장 위치 점검`);
  const pa = await session(au, ap);
  await pa.click('#menu-bar .rhwp-user-btn');
  await pa.waitForSelector('.rhwp-user-menu button:has-text("사용자 관리")');
  const [tab] = await Promise.all([pa.context().waitForEvent('page'), pa.click('.rhwp-user-menu button:has-text("사용자 관리")')]);
  await tab.waitForLoadState();
  ok(tab.url().endsWith('/rhwp-auth/admin'), `관리 화면 새 탭 (${tab.url()})`);
  const check = await tab.$eval('table', (t) => t.innerText);
  ok(check.includes('/files') && check.includes('/data') && check.includes(`공유 폴더 ${rw}`), '저장 위치 점검 표');
  ok(!check.includes('확인 필요'), '저장 위치 모두 정상');
  const [tab2] = await Promise.all([pa.context().waitForEvent('page'), (async () => { await menu(pa, 'ext:account'); })()]);
  await tab2.waitForLoadState();
  ok(tab2.url().endsWith('/rhwp-auth/'), `파일 메뉴 > 내 계정 → 새 탭 (${tab2.url()})`);
  const me2 = await api(pa, '/rhwp-auth/api/me');
  const del2 = await api(pa, '/rhwp-auth/api/fs/file?path=' + encodeURIComponent(`mount/${rw}/양식/작성본.hwp`),
    { method: 'DELETE', headers: { 'X-RHWP-CSRF': me2.body.csrf } });
  ok(del2.status === 204, `관리자는 공유 폴더 삭제 가능 (${del2.status})`);
} catch (e) {
  console.error('FAIL', e.message.split('\n')[0]);
  if (page) await page.screenshot({ path: 'mount-fail.png' }).catch(() => {});
  fail++;
} finally {
  await browser.close();
}
console.log(fail ? `FAIL (${fail})` : 'PASS');
process.exitCode = fail ? 1 : 0;
