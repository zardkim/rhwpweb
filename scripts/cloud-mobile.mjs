// 모바일(터치) 화면에서 서버 저장/열기 확인
import { chromium, devices } from 'playwright';

const [base, user, pass] = process.argv.slice(2);
const browser = await chromium.launch();
const ctx = await browser.newContext({ ...devices['Pixel 7'] });
const page = await ctx.newPage();
page.on('dialog', async (d) => { console.log(`  dialog: ${d.message().split('\n')[0]}`); await d.accept(); });
const ok = (c, m) => { console.log((c ? '  ✓ ' : '  ❌ ') + m); if (!c) process.exitCode = 1; };

try {
  await page.goto(base + '/rhwp/');
  await page.fill('input[name=username]', user);
  await page.fill('input[name=password]', pass);
  await Promise.all([page.waitForURL('**/rhwp/**'), page.tap('button[type=submit]')]);
  await page.waitForFunction(() => /\.hwp/.test(document.title), null, { timeout: 30000 });
  await page.screenshot({ path: 'm0-editor.png' });
  const fileMenu = page.locator('.menu-item[data-menu="file"]');
  ok(await fileMenu.isVisible(), '좁은 화면에서 [파일] 메뉴가 보임');
  await fileMenu.tap();
  const item = page.locator('[data-cmd="ext:cloud-save-as"]');
  ok(await item.isVisible(), '☁ 서버에 다른 이름으로 저장 항목이 보임');
  await page.screenshot({ path: 'm1-menu.png' });
  await item.tap();
  await page.waitForSelector('.rhwp-cloud input');
  await page.fill('.rhwp-cloud input', '모바일 문서');
  await page.locator('.rhwp-cloud button.primary').tap();
  await page.waitForSelector('.rhwp-cloud-toast', { timeout: 10000 });
  ok(true, '저장 토스트');

  await page.locator('.menu-item[data-menu="file"]').tap();
  await page.locator('[data-cmd="ext:cloud-open"]').tap();
  await page.waitForSelector('.rhwp-cloud .row');
  await page.screenshot({ path: 'm2-open.png' });
  await page.locator('.rhwp-cloud .row', { hasText: '모바일 문서.hwp' }).tap();
  await page.locator('.rhwp-cloud button.primary').tap();
  await page.locator('button:has-text("대체 글꼴로 보기")').tap({ timeout: 8000 }).catch(() => {});
  await page.waitForFunction(() => document.title.includes('모바일 문서.hwp') && !document.querySelector('.rhwp-cloud-back'), null, { timeout: 15000 });
  ok(true, '탭 → 열기 버튼으로 서버 문서 열림');
  await page.screenshot({ path: 'm3-opened.png' });
} catch (e) {
  console.error('FAIL', e.message.split('\n')[0]);
  await page.screenshot({ path: 'm-fail.png' }).catch(() => {});
  process.exitCode = 1;
} finally {
  await browser.close();
}
