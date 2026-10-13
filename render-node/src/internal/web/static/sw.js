/* TAC Ai 智能鏈 Service Worker：核心頁面與靜態資源離線快取。 */
const CACHE = 'tacm-v11';
const CORE = ['/static/css/style.css', '/static/tacm.svg'];
self.addEventListener('install', function(e){
  e.waitUntil(caches.open(CACHE).then(function(c){ return c.addAll(CORE); }).then(function(){ return self.skipWaiting(); }));
});
self.addEventListener('activate', function(e){
  e.waitUntil(caches.keys().then(function(keys){
    return Promise.all(keys.filter(function(k){ return k !== CACHE; }).map(function(k){ return caches.delete(k); }));
  }).then(function(){ return self.clients.claim(); }));
});
self.addEventListener('fetch', function(e){
  var u = new URL(e.request.url);
  if (e.request.method !== 'GET' || !u.protocol.startsWith('http')) return;
  // M34：API 一律網路直連（不緩存）——避免礦機/錢包/全鏈數據「固定不動」。
  if (u.pathname.indexOf('/api/') === 0) { e.respondWith(fetch(e.request)); return; }
  // M38.6：i18n 字典一律網路直連（不緩存）——避免部署後語言版本殘留舊快取。
  if (u.pathname === '/static/js/i18n.js') {
    e.respondWith(fetch(e.request).catch(function(){ return caches.match(e.request); }));
    return;
  }
  // M84：HTML（頁面導航）一律「網路直連、永不緩存」——舊版深色整頁 HTML 從此不可能再被快取餵出。
  if (e.request.mode === 'navigate') { e.respondWith(fetch(e.request)); return; }
  if (u.pathname.startsWith('/static/')) {
    // M83：靜態資源一律「網路優先」（network-first）——絕不讓舊版 CSS/JS 殘留深色主題；
    // 僅離線時才回退快取。新 SW 安裝時 activate 會清空所有舊 cache，一載入即全亮。
    e.respondWith(fetch(e.request).then(function(res){
      if (res.ok) { var clone = res.clone(); caches.open(CACHE).then(function(c){ c.put(e.request, clone); }); }
      return res;
    }).catch(function(){ return caches.match(e.request); }));
    return;
  }
  // 兜底：非 navigate 的其餘請求（極少數）——網路直連，不讀不寫快取。
  e.respondWith(fetch(e.request));
});
