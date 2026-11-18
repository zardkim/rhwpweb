// rhwpweb 이미지 브라우저 스모크 테스트 (Playwright, 헤드리스 Chromium)
//   node smoke.mjs http://127.0.0.1:PORT
// 통과 조건: 에디터가 빈 문서를 열고(WASM 초기화 성공), 페이지 오류·4xx/5xx 가 없다.
import { chromium } from 'playwright';

const base = process.argv[2];
const browser = await chromium.launch();
const page = await browser.newPage();
const problems = [];
page.on('pageerror', (e) => problems.push('pageerror: ' + e.message));
// ERR_NETWORK_CHANGED: 호스트 네트워크에서 다른 컨테이너가 생기고 없어질 때 Chromium 이 내는 일시 오류 (이미지와 무관)
page.on('console', (m) => { if (m.type() === 'error' && !/ERR_NETWORK_CHANGED/.test(m.text())) problems.push('console: ' + m.text()); });
page.on('response', (r) => { if (r.status() >= 400) problems.push(`HTTP ${r.status()} ${r.url()}`); });

await page.goto(base + '/rhwp/', { waitUntil: 'networkidle', timeout: 60000 });
// 빈 문서가 만들어지면 제목이 "새 문서.hwp - rhwp-studio" 가 된다 (WASM 이 떠야 가능)
await page.waitForFunction(() => /\.hwp/.test(document.title), null, { timeout: 30000 })
  .catch(() => problems.push('timeout: 빈 문서가 열리지 않음 (WASM 초기화 실패?)'));
const title = await page.title();
const wasm = await page.evaluate(() =>
  performance.getEntriesByType('resource').filter((e) => e.name.endsWith('.wasm')).length);
if (!wasm) problems.push('WASM 을 받지 않음');
// 서버 저장 메뉴(web/cloud/cloud.js)가 붙었는지: 업스트림 확장 API 가 바뀌면 여기서 걸린다
if (process.env.SMOKE_CLOUD !== '0') {
  const cloud = await page.waitForFunction(() => {
    const a = window.rhwpStudio && window.rhwpStudio.automation;
    if (!a) return false;
    const ids = a.listCommands().map((c) => c.id);
    const menu = document.querySelector('[data-cmd="ext:cloud-open"], [data-command="ext:cloud-open"]');
    const btn = document.querySelector('#menu-bar .rhwp-user-btn');
    return ['ext:cloud-open', 'ext:cloud-save', 'ext:cloud-save-as', 'ext:account-logout', 'ext:account-admin']
      .every((i) => ids.includes(i)) && (menu ? (btn ? 'menu' : 'nobutton') : 'nomenu');
  }, null, { timeout: 15000 }).then((h) => h.jsonValue()).catch(() => null);
  if (cloud !== 'menu') problems.push(`서버 저장 메뉴(ext:cloud-*)가 붙지 않음 (${cloud})`);
  else console.log(`cloud=${cloud}`);
}
await browser.close();

console.log(`title=${JSON.stringify(title)} wasm=${wasm}`);
if (problems.length) {
  console.error('FAIL\n  ' + problems.join('\n  '));
  process.exit(1);
}
console.log('PASS');
