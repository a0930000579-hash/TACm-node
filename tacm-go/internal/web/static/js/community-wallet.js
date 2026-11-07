// community-wallet.js — 社群模組統一權威地址（比照交易所 v160 錢包中心）。
//
// 權威優先級（使用者硬性要求）：
//   ① localStorage tac_ex_wallet.addr（tx0 開頭，交易所錢包中心明確選擇的地址）
//   ② /api/auth/me 回傳的 user.wallet_addr（tx0 開頭，已登入會員）
//   ③ ensureGuestAddr()（base.html 全域；其內部已先讀 tac_ex_wallet）
// 全部失敗回 ''。cm_id 僅作為鏡像（同步寫回供舊邏輯相容），不再作為首要來源。
(function () {
  'use strict';

  // 僅接受當前鏈 tx0 格式；發現非 tx0/舊鏈字串一律不採用（回退權威來源）。
  function isTx0(a) { return typeof a === 'string' && a.indexOf('tx0') === 0; }

  // cmResolveAddr：依序回傳第一個有效的 tx0 地址；全部失敗回 ''。
  // 完整 try/catch，不吞錯、不產生未捕獲 promise。
  async function cmResolveAddr() {
    // ① 交易所錢包中心明確選擇的地址
    try {
      var raw = null;
      try { raw = localStorage.getItem('tac_ex_wallet'); } catch (e) {}
      if (raw) {
        var ex = JSON.parse(raw);
        if (ex && isTx0(ex.addr)) return ex.addr;
      }
    } catch (e) { /* 轉下一來源 */ }

    // ② 已登入會員的鏈上錢包地址
    try {
      var r = await fetch('/api/auth/me', { credentials: 'same-origin', cache: 'no-store' });
      var j = await r.json().catch(function () { return null; });
      if (j && j.ok && j.logged_in && j.user && isTx0(j.user.wallet_addr)) {
        return j.user.wallet_addr;
      }
    } catch (e) { /* 轉下一來源 */ }

    // ③ 訪客地址（base.html 全域；其內部已先讀 tac_ex_wallet）
    try {
      if (typeof ensureGuestAddr === 'function') {
        var g = await ensureGuestAddr();
        if (isTx0(g)) return g;
      }
    } catch (e) { /* 回傳空 */ }

    return '';
  }

  var cmCurrent = '';      // 目前解析到的權威地址（隱藏變數）
  var cmLastApplied = null; // 上次已套用的地址（避免 focus 重複觸發頁面 reload）
  var cmInflight = false;
  var cmOpts = null;

  function resolveEl(x) {
    if (!x) return null;
    return (typeof x === 'string') ? document.querySelector(x) : x;
  }

  function apply(addr) {
    // 非 tx0/舊鏈字串不採用 → 回退為空。
    if (!isTx0(addr)) addr = '';
    cmCurrent = addr;
    window.cmAddr = addr;
    if (addr) { try { localStorage.setItem('cm_id', addr); } catch (e) {} }

    // 把地址寫入手動輸入框（唯讀，標示「以錢包中心為準」）。
    try {
      var inp = resolveEl(cmOpts && cmOpts.input);
      if (inp) {
        inp.value = addr;
        inp.readOnly = true;
        inp.setAttribute('data-cm-auth', '1');
        if (window.TAC_TE) inp.title = TAC_TE('以錢包中心為準');
      }
    } catch (e) {}

    // 一併寫入其他顯示元素。
    try {
      if (cmOpts && cmOpts.labels && cmOpts.labels.length) {
        cmOpts.labels.forEach(function (sel) {
          var el = resolveEl(sel);
          if (el) el.textContent = addr;
        });
      }
    } catch (e) {}

    // 地址變化（或首次）才通知頁面重新載入，避免 focus 頻繁觸發 reload。
    if (addr !== cmLastApplied) {
      cmLastApplied = addr;
      try { if (cmOpts && typeof cmOpts.onResolve === 'function') cmOpts.onResolve(addr); } catch (e) {}
    }
  }

  function run() {
    if (cmInflight) return;
    cmInflight = true;
    cmResolveAddr()
      .then(function (addr) { cmInflight = false; apply(addr); })
      .catch(function () { cmInflight = false; apply(cmCurrent); });
  }

  // cmBindAddr(opts)：進頁解析後把地址寫入顯示元素/隱藏變數，同步 cm_id（鏡像），
  // 並監聽 window 'storage'、'pageshow'、'focus' 重新解析——錢包中心更換後回到社群頁顯示同一地址。
  //   opts.input      手動地址輸入框（selector 或 element）→ 填入權威地址並唯讀
  //   opts.labels     要一併顯示地址的元素（selector/element 陣列）
  //   opts.onResolve(addr) 解析完成（且地址變化時）回呼；頁面在此設定自己的 me 並重新載入
  function cmBindAddr(opts) {
    cmOpts = opts || {};
    var start = function () { run(); };
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', start);
    } else { start(); }

    // 錢包中心（另一分頁）更換 tac_ex_wallet/tac_guest_addr → 重新解析。
    window.addEventListener('storage', function (e) {
      if (!e.key || e.key === 'tac_ex_wallet' || e.key === 'tac_guest_addr' ||
          e.key === 'tac_defi_addr' || e.key === 'cm_id') { run(); }
    });
    // bfcache 還原 / 分頁重新聚焦 → 重新解析（回到社群頁顯示錢包中心同一地址）。
    window.addEventListener('pageshow', run);
    window.addEventListener('focus', run);
  }

  window.cmResolveAddr = cmResolveAddr;
  window.cmBindAddr = cmBindAddr;
  window.cmAddr = '';
})();
