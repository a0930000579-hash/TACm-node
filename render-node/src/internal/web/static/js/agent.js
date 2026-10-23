// TACm AI Agent 前端（v163）。
// 與後端 POST /api/ai/agent 對接：顯示工具呼叫軌跡、寫操作二次確認閘。
// 錢包地址沿用全站權威 localStorage tac_ex_wallet（ensureGuestAddr 已同步）。
(function () {
  "use strict";

  var chat = document.getElementById("agChat");
  var q = document.getElementById("agQ");
  var send = document.getElementById("agSend");
  if (!chat || !q || !send) return;

  var SESSION = "web-" + Date.now();
  var walletAddr = "";

  function curLang() { return (window.__TAC_LANG__ === "zh") ? "zh" : "en"; }
  function t(zh, en) { return curLang() === "zh" ? zh : en; }

  // SVG icons per step kind（禁 emoji）。
  function icon(kind) {
    var stroke = "#1877f2";
    var path = "<circle cx='12' cy='12' r='8'/>";
    if (kind === "tool") path = "<path d='M13 2L4 14h6l-1 8 9-12h-6z'/>";
    else if (kind === "thought") path = "<circle cx='12' cy='12' r='8'/><circle cx='12' cy='12' r='2'/>";
    else if (kind === "error") { stroke = "#c0392b"; path = "<circle cx='12' cy='12' r='8'/><path d='M9 9l6 6M15 9l-6 6'/>"; }
    else if (kind === "needs_confirm") { stroke = "#b45309"; path = "<path d='M12 3l9 16H3z'/><path d='M12 10v4M12 17v.5'/>"; }
    return "<svg class='ico' viewBox='0 0 24 24' fill='none' stroke='" + stroke + "' stroke-width='1.8' stroke-linecap='round' stroke-linejoin='round' aria-hidden='true'>" + path + "</svg>";
  }

  function addMsg(text, who) {
    var div = document.createElement("div");
    div.className = "ag-msg " + who;
    div.textContent = text;
    chat.appendChild(div);
    chat.scrollTop = chat.scrollHeight;
    return div;
  }

  function renderTrace(traceEl, trace) {
    if (!trace || !trace.length) return;
    var det = document.createElement("details");
    det.className = "ag-trace";
    det.open = true;
    var sum = document.createElement("summary");
    sum.innerHTML = icon("thought") + "<span>" + t("工具呼叫軌跡", "Tool-call trace") + " (" + trace.length + ")</span>";
    det.appendChild(sum);
    trace.forEach(function (st) {
      var row = document.createElement("div");
      row.className = "ag-step " + (st.kind === "error" ? "err" : st.kind === "needs_confirm" ? "confirm" : "");
      var label = st.kind;
      if (st.tool) label = st.tool;
      var html = icon(st.kind) + "<div><span class='lbl'>" + escapeHtml(label) + "</span>";
      if (st.thought) html += "<div>" + escapeHtml(st.thought) + "</div>";
      if (st.args && Object.keys(st.args).length) html += "<pre>" + escapeHtml(JSON.stringify(st.args)) + "</pre>";
      if (st.result) html += "<pre>" + escapeHtml(st.result) + "</pre>";
      if (st.error) html += "<pre style='color:#c0392b'>" + escapeHtml(st.error) + "</pre>";
      html += "</div>";
      row.innerHTML = html;
      det.appendChild(row);
    });
    traceEl.appendChild(det);
  }

  function escapeHtml(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function ask(text, confirm, pendingEl) {
    var typing = document.createElement("div");
    typing.className = "ag-typing";
    typing.textContent = t("思考中…", "Thinking…");
    chat.appendChild(typing);
    chat.scrollTop = chat.scrollHeight;

    var body = { message: text, address: walletAddr, lang: curLang(), session_id: SESSION, confirm: !!confirm };
    fetch("/api/ai/agent", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })
      .then(function (r) { return r.json().then(function (j) { return { status: r.status, body: j }; }); })
      .then(function (res) {
        if (typing.parentNode) typing.parentNode.removeChild(typing);
        if (pendingEl && pendingEl.parentNode) pendingEl.parentNode.removeChild(pendingEl);
        if (res.status !== 200 || !res.body || !res.body.ok) {
          addMsg((res.body && res.body.error) ? res.body.error : t("發生錯誤", "Error"), "err");
          return;
        }
        // 軌跡卡片掛在最終回答之前。
        var traceWrap = document.createElement("div");
        chat.appendChild(traceWrap);
        renderTrace(traceWrap, res.body.trace);

        if (res.body.needs_confirm) {
          var box = document.createElement("div");
          box.className = "ag-confirm";
          box.innerHTML = "<div>" + escapeHtml(res.body.answer || t("需要確認", "Confirmation required")) + "</div>" +
            "<div class='row'><button class='ok'>" + t("確認執行", "Confirm") + "</button>" +
            "<button class='cancel'>" + t("取消", "Cancel") + "</button></div>";
          chat.appendChild(box);
          chat.scrollTop = chat.scrollHeight;
          box.querySelector(".ok").addEventListener("click", function () {
            box.querySelector(".ok").disabled = true;
            ask(text, true, box);
          });
          box.querySelector(".cancel").addEventListener("click", function () {
            if (box.parentNode) box.parentNode.removeChild(box);
          });
          return;
        }
        addMsg(res.body.answer || "", "bot");
      })
      .catch(function (e) {
        if (typing.parentNode) typing.parentNode.removeChild(typing);
        addMsg("Error: " + e, "err");
      });
  }

  function sendCurrent() {
    var v = q.value.trim();
    if (!v) return;
    q.value = "";
    addMsg(v, "user");
    ask(v, false, null);
  }

  send.addEventListener("click", sendCurrent);
  q.addEventListener("keydown", function (e) { if (e.key === "Enter") sendCurrent(); });

  var chips = document.querySelectorAll(".ag-chip");
  for (var i = 0; i < chips.length; i++) {
    chips[i].addEventListener("click", function () {
      var v = this.getAttribute("data-q");
      addMsg(v, "user");
      ask(v, false, null);
    });
  }

  // 初始化地址。
  if (typeof ensureGuestAddr === "function") {
    ensureGuestAddr().then(function (addr) {
      walletAddr = addr || "";
      document.getElementById("agAddrVal").textContent = walletAddr || "—";
    }).catch(function () {});
  }

  addMsg(t("你好，我是 TACm AI Agent。我能查詢你的鏈上餘額、行情、最近交易，並在你確認後準備 DEX 兌換與 DeFi 動作。",
    "Hi, I'm the TACm AI Agent. I can check your on-chain balance, prices and recent transactions, and prepare DEX swaps / DeFi actions after your confirmation."), "bot");
})();
