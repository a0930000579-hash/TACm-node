/* TAC Ai Smart Chain — Node Console (M86) */
"use strict";
(function () {
  var dict = {
    zh: {
      "n.brandSub": "驗證節點控制台",
      "n.synced": "已同步",
      "n.syncing": "同步中",
      "n.behind": "落後 {n} 塊",
      "n.openMainnet": "開啟主網",
      "n.heroEyebrow": "TAC Ai 智能鏈 · 主網",
      "n.heroTitle": "驗證節點<br>控制台",
      "n.heroDesc": "運行 TAC 節點、守護網路、驗證區塊並獲得 9% 出塊獎勵——全部在專屬的節點品牌面板完成。",
      "n.cTip": "鏈頂高度",
      "n.cTipSub": "最新區塊高度",
      "n.cFinal": "最終化",
      "n.cFinalSub": "BFT 最終化高度",
      "n.cDiff": "動態難度",
      "n.cDiffSub": "動態 PoW 難度",
      "n.cSync": "同步進度",
      "n.cSyncSub": "落後最佳節點塊數",
      "n.pNode": "節點身分",
      "n.kAddress": "節點地址",
      "n.kNodeId": "節點 ID",
      "n.kIdentity": "身分金鑰",
      "n.kUptime": "運行時間",
      "n.kBaseFee": "基礎費",
      "n.kConsensus": "共識機制",
      "n.kReward": "節點獎勵",
      "n.rewardDesc": "每個區塊鑄造 10 TACM：9% 歸出塊節點、16% 挹注獎勵池、75% 依在線算力分配給礦工。",
      "n.pPeers": "P2P 節點",
      "n.tNode": "節點",
      "n.tHeight": "高度",
      "n.tFinal": "最終化",
      "n.tState": "狀態",
      "n.pValidators": "驗證人",
      "n.tAddress": "地址",
      "n.tPower": "權重",
      "n.pBlocks": "最新區塊",
      "n.tProposer": "出塊者",
      "n.tTx": "交易數",
      "n.tDiff": "難度",
      "n.pJoin": "運行你自己的節點",
      "n.oneClick": "一鍵安裝（三平台通用）", "n.oneClickTip": "自動偵測 systemd / nohup / Render、安裝 Go、編譯、啟動並輸出控制台網址。",
      "n.github": "原始碼永久固化（GitHub）",
      "n.join1": "下載節點套件並在伺服器上解壓。",
      "n.join2": "使用同一創世錨點啟動：",
      "n.join3": "觀察此面板——節點會自動同步並開始驗證區塊。",
      "n.join4": "每塊獲得 9% 節點獎勵，可由節點金鑰提取。",
      "n.footLine": "驗證節點控制台",
      "n.online": "在線",
      "n.offline": "離線",
      "n.you": "本機"
    },
    en: {
      "n.brandSub": "Validator Node Console",
      "n.synced": "Synced",
      "n.syncing": "Syncing",
      "n.behind": "{n} blocks behind",
      "n.openMainnet": "Open Mainnet",
      "n.heroEyebrow": "TAC Ai Smart Chain · Mainnet",
      "n.heroTitle": "Validator Node<br>Console",
      "n.heroDesc": "Run a TAC node, secure the network, validate blocks and earn the 9% block reward — all from your own branded node dashboard.",
      "n.cTip": "Chain Tip",
      "n.cTipSub": "Latest block height",
      "n.cFinal": "Finalized",
      "n.cFinalSub": "BFT finalized height",
      "n.cDiff": "Difficulty",
      "n.cDiffSub": "Dynamic PoW difficulty",
      "n.cSync": "Sync Progress",
      "n.cSyncSub": "Blocks behind best peer",
      "n.pNode": "Node Identity",
      "n.kAddress": "Node Address",
      "n.kNodeId": "Node ID",
      "n.kIdentity": "Identity Key",
      "n.kUptime": "Uptime",
      "n.kBaseFee": "Base Fee",
      "n.kConsensus": "Consensus",
      "n.kReward": "Node Reward",
      "n.rewardDesc": "Every block mints 10 TACM: 9% goes to the producing node, 16% to the reward pool, 75% split among online miners by hashrate.",
      "n.pPeers": "P2P Peers",
      "n.tNode": "Node",
      "n.tHeight": "Height",
      "n.tFinal": "Finalized",
      "n.tState": "State",
      "n.pValidators": "Validators",
      "n.tAddress": "Address",
      "n.tPower": "Power",
      "n.pBlocks": "Latest Blocks",
      "n.tProposer": "Proposer",
      "n.tTx": "Txs",
      "n.tDiff": "Diff",
      "n.pJoin": "Run Your Own Node",
      "n.oneClick": "One-Click Install (all platforms)", "n.oneClickTip": "Auto-detects systemd / nohup / Render, installs Go, builds, starts and prints your console URL.",
      "n.github": "Source Code Permanent (GitHub)",
      "n.join1": "Download the node package and unpack it on your server.",
      "n.join2": "Start with the same genesis anchor:",
      "n.join3": "Watch this dashboard — your node will sync automatically and start validating blocks.",
      "n.join4": "Earn the 9% node reward per block, withdrawable by the node key.",
      "n.footLine": "Validator Node Console",
      "n.online": "online",
      "n.offline": "offline",
      "n.you": "self"
    }
  };
  var cur = localStorage.getItem("tac_lang") || "en";
  if (cur !== "zh") cur = "en";

  function apply() {
    document.documentElement.lang = cur;
    document.querySelectorAll("[data-i18n]").forEach(function (el) {
      var key = el.getAttribute("data-i18n");
      var t = dict[cur][key];
      if (t === undefined) return;
      if (el.tagName === "INPUT" || el.tagName === "TEXTAREA") {
        el.placeholder = t;
      } else {
        el.innerHTML = t;
      }
    });
    document.getElementById("langToggle").textContent = cur === "zh" ? "English" : "中文";
  }

  function t(key, sub) {
    var s = dict[cur][key] || key;
    if (sub) Object.keys(sub).forEach(function (k) { s = s.replace("{" + k + "}", sub[k]); });
    return s;
  }

  function $(id) { return document.getElementById(id); }
  function esc(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }
  function short(s, n) {
    if (!s) return "—";
    n = n || 12;
    return s.length > n * 2 + 3 ? s.slice(0, n) + "…" + s.slice(-n) : s;
  }
  function ts(sec) {
    if (!sec) return "—";
    var d = new Date(sec * 1000);
    return d.toISOString().replace("T", " ").slice(0, 19);
  }

  var state = { mainnetURL: "" };

  function renderStatus(st) {
    $("tip").textContent = st.block_height;
    $("final").textContent = st.final_block_height;
    $("diff").textContent = st.effective_difficulty;
    $("behind").textContent = st.synced ? "0" : st.behind_blocks;
    $("nodeAddr").textContent = st.address;
    $("nodeId").textContent = short(st.node_id, 16);
    $("identity").textContent = short(st.identity_pub_hex, 14);
    $("uptime").textContent = st.uptime;
    $("baseFee").textContent = st.base_fee;
    $("consensus").textContent = st.consensus;
    state.mainnetURL = st.mainnet_url;
    var links = document.querySelectorAll("#mainnetLink, #footMainnet");
    links.forEach(function (a) { a.href = st.mainnet_url; });
    var pill = $("syncPill");
    if (st.synced) {
      pill.className = "pill pill-ok";
      pill.textContent = t("n.synced");
    } else {
      pill.className = "pill pill-warn";
      pill.textContent = t("n.syncing") + " · " + t("n.behind", { n: st.behind_blocks });
    }
  }

  function renderPeers(peers) {
    var on = peers.filter(function (p) { return p.online; }).length;
    $("peerCount").textContent = on;
    var tb = document.querySelector("#peerTbl tbody");
    tb.innerHTML = "";
    if (!peers.length) {
      var tr = document.createElement("tr");
      tr.innerHTML = '<td colspan="4" style="color:var(--muted)">—</td>';
      tb.appendChild(tr);
      return;
    }
    peers.forEach(function (p) {
      var tr = document.createElement("tr");
      var mark = p.online ? '<span class="dot-on">●</span>' : '<span class="dot-off">●</span>';
      tr.innerHTML =
        "<td>" + short(p.node_id, 10) + (p.owner === "self" ? " · " + esc(t("n.you")) : "") + "</td>" +
        "<td>" + p.height + "</td>" +
        "<td>" + p.finalized + "</td>" +
        "<td>" + mark + " " + esc(p.online ? t("n.online") : t("n.offline")) + "</td>";
      tb.appendChild(tr);
    });
  }

  function renderValidators(v) {
    $("valCount").textContent = v.total + " · Σ" + v.power + " · quorum " + v.quorum;
    var tb = document.querySelector("#valTbl tbody");
    tb.innerHTML = "";
    v.validators.forEach(function (val) {
      var tr = document.createElement("tr");
      tr.innerHTML = "<td>" + esc(val.address) + "</td><td>" + val.power + "</td>";
      tb.appendChild(tr);
    });
  }

  function renderBlocks(b) {
    var tb = document.querySelector("#blockTbl tbody");
    tb.innerHTML = "";
    if (!b.blocks.length) {
      var tr = document.createElement("tr");
      tr.innerHTML = '<td colspan="4" style="color:var(--muted)">—</td>';
      tb.appendChild(tr);
      return;
    }
    b.blocks.forEach(function (blk) {
      var tr = document.createElement("tr");
      tr.innerHTML =
        "<td>#" + blk.height + "</td>" +
        "<td>" + short(blk.proposer, 10) + "</td>" +
        "<td>" + blk.tx_count + "</td>" +
        "<td>" + blk.difficulty + "</td>";
      tb.appendChild(tr);
    });
  }

  function poll() {
    fetch("/api/status", { cache: "no-store" })
      .then(function (r) { if (!r.ok) throw new Error("status"); return r.json(); })
      .then(renderStatus)
      .catch(function () { $("syncPill").className = "pill pill-err"; $("syncPill").textContent = "error"; });
    fetch("/api/peers", { cache: "no-store" })
      .then(function (r) { if (!r.ok) throw new Error("peers"); return r.json(); })
      .then(function (d) { renderPeers(d.peers || []); })
      .catch(function () {});
    fetch("/api/validators", { cache: "no-store" })
      .then(function (r) { if (!r.ok) throw new Error("validators"); return r.json(); })
      .then(renderValidators)
      .catch(function () {});
    fetch("/api/blocks", { cache: "no-store" })
      .then(function (r) { if (!r.ok) throw new Error("blocks"); return r.json(); })
      .then(renderBlocks)
      .catch(function () {});
  }

  document.getElementById("langToggle").addEventListener("click", function () {
    cur = cur === "zh" ? "en" : "zh";
    localStorage.setItem("tac_lang", cur);
    apply();
    poll();
  });

  apply();
  poll();
  setInterval(poll, 10000);
})();
