// RHWP_PWA=0 빌드용 kill-switch 서비스 워커.
// 예전 버전이 설치한 서비스 워커(오프라인 캐시)는 로그인 확인 없이 앱을 띄우므로,
// 업데이트 확인 때 이 파일을 받으면 캐시를 지우고 스스로 해제한다.
self.addEventListener('install', function () { self.skipWaiting(); });
self.addEventListener('activate', function (event) {
  event.waitUntil((async function () {
    var keys = await caches.keys();
    await Promise.all(keys.filter(function (k) {
      return k.indexOf('workbox') === 0 || k === 'wasm-cache';
    }).map(function (k) { return caches.delete(k); }));
    await self.registration.unregister();
    var clients = await self.clients.matchAll({ type: 'window' });
    clients.forEach(function (c) { c.navigate(c.url); });
  })());
});
