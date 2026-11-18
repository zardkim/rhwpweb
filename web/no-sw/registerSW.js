// RHWP_PWA=0: 서비스 워커를 등록하지 않고, 남아 있는 /rhwp/ 서비스 워커는 해제한다.
// (서비스 워커 캐시는 로그인 확인 없이 앱을 띄우므로 로그인·사용자 관리와 함께 쓰지 않는다)
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.getRegistrations().then(function (regs) {
    regs.forEach(function (r) { if (r.scope.indexOf('/rhwp/') >= 0) r.unregister(); });
  });
}
