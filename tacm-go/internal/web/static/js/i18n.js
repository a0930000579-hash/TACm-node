// M39：TAC 鏈全套中英介面——預設英文，中文為第二語言（可切換）。
// 機制：data-i18n 精確替換 + TreeWalker 全域文字節點替換（雙向字典）+ TAC_T() 供 JS 動態字串使用。
(function () {
  'use strict';
  var dict = {
    zh: {
      'nav.home': '首頁', 'nav.wallet': '錢包',
      'ls.home': '首頁', 'ls.defi': 'DeFi', 'ls.stake': '質押', 'ls.liquid': '流動性質押', 'ls.gov': '治理', 'ls.apply': '套用',
      'ls.heroTitle': 'PoT 流動性質押（Proof-of-Trust Liquid Staking）', 'ls.heroDesc': '存入 TACm 獲得 stTACm 流動憑證——憑證餘額隨池內委託獎勵依匯率增長，可隨時贖回，也可用於 DeFi。質押即流動，資產不鎖倉。',
      'ls.tvl': '池內總質押 (TACm)', 'ls.stSupply': 'stTACm 流通量', 'ls.rate': '兌換率', 'ls.apy': '預估年化 (APY)', 'ls.depositors': '參與帳戶', 'ls.rateChart': '匯率實時曲線 (stTACm/TACm)', 'ls.rateNote': '每 10 秒取樣一次（本機歷史），匯率隨池委託獎勵注入上升。',
      'ls.tiusdTitle': 'TiUSD 取得（穩定幣 1:1 錨定 USDT）', 'ls.tiusdDesc': 'TiUSD 非免費水龍頭資產：① DeFi 超額抵押 CDP 抵押 TACm 鑄造（1:1 錨定）；② 交易所穩定幣兌換 USDT ↔ TiUSD（0.5% 費）；③ 質押收益累積 TACm 後經 CDP 換取。', 'ls.goDefi': '前往 DeFi CDP 鑄造', 'ls.goSwap': '前往穩定幣兌換', 'ls.stakeTitle': '質押 TACm → 得 stTACm', 'ls.address': '你的地址', 'ls.amount': '質押數量 (TACm)', 'ls.stakeBtn': '質押',
      'ls.redeemTitle': '贖回 stTACm → TACm', 'ls.address2': '你的地址', 'ls.amount2': '贖回數量 (stTACm)', 'ls.redeemBtn': '贖回',
      'ls.myTitle': '我的持倉', 'ls.query': '查詢', 'ls.myStaked': '質押本金 (TACm)', 'ls.mySt': 'stTACm 餘額', 'ls.myRedeem': '可贖回 (TACm)',
      'ls.howTitle': '運作方式', 'ls.step': '步驟', 'ls.desc': '說明',
      'ls.how1': '存入 TACm，系統按現行匯率鑄造 stTACm 流動憑證（初期 1:1）。',
      'ls.how2': '池內資產參與節點委託，委託獎勵每區塊自動注入池中（v75），匯率逐步上升——你的 stTACm 贖回值隨之增長。',
      'ls.how3': '隨時依現行匯率贖回 TACm；stTACm 也可用於其他 DeFi 協議。',
      'wallet.onchain': ' 我的鏈上紀錄', 'nav.exchange': '交易所', 'nav.dash': '儀表板',
      'nav.mining': '挖礦', 'nav.community': '社群', 'nav.ai': 'Titan Ai', 'nav.tokens': '代幣', 'nav.nfts': 'NFT', 'nav.defi': 'DeFi', 'nav.tiusd': 'TiUSD 監控', 'nav.ls': '質押', 'nav.governance': '治理',
      'nav.dapp': 'DApp 商店', 'nav.agent': 'AI Agent', 'pill.dapp': 'DApp 商店', 'pill.agent': 'AI Agent',
      'nh.title': '網路健康',
      'nh.sub': '全網節點健康儀表 · 在線狀態 / 最後心跳 / 出塊接管',
      'tx.logs': '事件日誌', 'tx.logAddr': '合約地址', 'tx.decodedEvents': '事件解碼（ABI）', 'tx.gasUsed': 'Gas 使用量', 'tx.transfers': '代幣轉移', 'tx.tContract': '合約', 'tx.tValue': '數量 / Token ID', 'tx.tBlock': '區塊', 'tx.hash': '交易哈希', 'addr.transfers': '代幣轉移', 'fc.title': '測試幣水龍頭', 'fc.sub': '開發者生態：為你的地址免費領取測試 TACm', 'fc.claim': '領取測試幣', 'fc.limit': '每地址每日 {{ limit }} TACm', 'fc.disabled': '主網已停用（免費資產不再發放）', 'fc.guide': 'TiUSD / USDT 流通', 'fc.guide1': '① 水龍頭領 TACm → 交易所賣 TACM/TiUSD 買 TiUSD（穩定幣 1:1 錨定 USDT）。', 'fc.guide2': '② DeFi 借貸（CDP）：抵押 TACm 鑄造 TiUSD。', 'fc.guide3': '③ TACm/TiUSD → USDT：交易所 TACM/USDT、TiUSD/USDT 市場（掛單/市價/閃兌）。', 'fc.guide4': '④ 跨鏈：TACM/TiUSD/USDT 皆可經多鏈橋轉移至 BSC/Ethereum 等主流鏈。', 'fc.limitNote': '每地址每日限額', 'fc.addrLabel': '你的 tx0 地址', 'fc.claimBtn': '領取', 'fc.how': '如何使用', 'fc.step1': '填入你的錢包地址後點擊領取', 'fc.step2': '交易由節點金鑰簽發並在數秒內上鏈', 'fc.step3': '到錢包或區塊瀏覽器確認到帳（每日一次）',
      'pill.ai': 'Titan Ai',
      'ai_assistant': 'Titan Ai', 'ai_sub': '泰坦 Ai —— 鏈上智能助手，問鏈上數據即時回答',
      'ai.compliance': '寫操作需你確認才執行 · 節點不代簽 · 投資有風險',
      'ai.modeAssist': '助手', 'ai.modeAgent': 'Agent',
      'ai.marketCard': 'AI 模型市場', 'ai.marketCardSub': '鏈上註冊／訂閱／呼叫模型',
      'ai.verifyCard': '跨鏈 AI 驗證', 'ai.verifyCardSub': '推理結果驗證網路',
      'ai.wallet': '錢包：', 'ai.engine': 'AI 引擎：', 'ai.probing': '偵測中…',
      'ai_suggest_status': '節點狀態', 'ai_suggest_block': '最新區塊', 'ai_suggest_audit': '鏈上審計',
      'ai_suggest_reward': '獎勵分潤', 'ai_suggest_miner': '在線礦工', 'ai_suggest_supply': '總供應',
      'ai_suggest_join': '加入節點', 'ai_suggest_transfer': '轉帳指引',
      'ai_placeholder': '輸入問題…（例如：節點狀態）', 'ai_send': '發送',
      'pill.standby': '待機', 'pill.mining': '挖礦中', 'pill.login': '登入/註冊',
      'pill.tools': '☰ 工具', 'pill.wp': '白皮書', 'pill.defi': 'DeFi', 'pill.assets': '資產中心', 'pill.c2c': 'C2C', 'pill.tokens': '代幣', 'pill.nfts': 'NFT', 'pill.en': 'EN', 'pill.lang': '中',
      'mining.start': '⚡ 開機挖礦', 'mining.stop': '停止挖礦', 'mining.online': '在線挖礦中', 'mining.offline': '離線',
      'mining.mech1': '・每區塊獎勵依供應模型動態計算（首年約 1.5 TACm、逐年衰減 25%，6 年總供應 5,200 萬封頂）：節點 45% 由鏈上在線節點（PoT 時間權證）均分，池 15% 自動挹注獎勵池，其餘 40% 按在線礦工算力占比即時瓜分。',
      'mining.mech2': '・礦機以地址註冊並持續心跳（hb:on），出塊時自動入帳；離線超過約 1 小時（3700 個區塊）不參與瓜分。節點無需質押即可註冊公鑰（node:reg）參與 PoT 出塊——時間面前人人平等。',
      'mining.mech3': '・共識為 PoT＋PoS 混合（時間權證＋質押節點）：質押 1,000 TACm 成為節點輪值出塊；未質押節點依在線時長累積出塊權重（每 6 小時 +1，上限 10）。',
      'pill.miner': '礦機狀態', 'nav.main': '主導航', 'explorer.searchPh': '輸入區塊高度、交易哈希或 tx0 地址…',
      'w3.title': 'MetaMask 接入', 'wallet.intro': '透過自訂 RPC 將 TACm 主網加入 MetaMask / Trust Wallet —— 完整支援 EIP-1559（Type 2）交易、事件日誌與交易收據。',
      'wallet.rpc': 'RPC URL', 'wallet.chainId': 'Chain ID', 'wallet.symbol': '代幣符號', 'wallet.decimals': '小數位',
      'wallet.add': '加到 MetaMask', 'wallet.verify': '驗證連線',
      'web3.connect': '連結錢包', 'web3.addr': '地址', 'web3.balance': '餘額',
      'web3.noMeta': '未偵測到 MetaMask，請先安裝後重試', 'web3.connectFail': '連線失敗',
      'tacns.title': '鏈上命名服務（.tac）', 'tacns.intro': '註冊 .tac 名稱並綁定地址——名稱↔地址雙向解析。',
      'tacns.status': '註冊表狀態', 'tacns.contract': '合約地址', 'tacns.deploy': '部署註冊表',
      'tacns.registerTitle': '註冊名稱', 'tacns.register': '註冊', 'tacns.ownerPh': 'tx0…（owner 地址）',
      'tacns.years': '年數', 'tacns.queryTitle': '查詢', 'tacns.query': '查詢', 'tacns.queryPh': 'label（如 titan）或 tx0 地址',
      'tacns.deployed': '已部署', 'tacns.undeployed': '未部署', 'tacns.regOk': '註冊已提交（等待入塊）',
      'tacns.regErr': '註冊失敗', 'tacns.fillReg': '請填寫名稱、owner 地址與年數', 'tacns.qEmpty': '請輸入 label 或地址',
      'tacns.noOwner': '未註冊', 'tacns.expiry': '到期（unix）', 'tacns.owner': '擁有者', 'tacns.resolved': '解析地址',
      'tacns.needDeploy': '請先部署註冊表', 'tacns.deployOk': '部署交易已提交', 'tacns.deployErr': '部署失敗',
      'tacns.calldataNote': 'setAddr / transfer / renew 請以自備私鑰簽名後 POST /tx/submit 上鏈',
      'wallet.multiTitle': '多簽合約錢包', 'wallet.intro': 'N/M 門檻多簽錢包——多簽管理資金，支援 EIP-1271 合約簽名驗證。',
      'wallet.status': '錢包狀態', 'wallet.contract': '合約地址', 'wallet.owners': 'Owner 數', 'wallet.threshold': '門檻',
      'wallet.deploy': '部署錢包', 'wallet.deployed': '已部署', 'wallet.undeployed': '未部署',
      'wallet.deployOk': '部署交易已提交', 'wallet.deployErr': '部署失敗',
      'wallet.ownerMgmt': 'Owner 管理', 'wallet.ownerPh': 'owner 地址（tx0 或 0x）',
      'wallet.hdTitle': '金鑰管理（HD）', 'wallet.hdHint': '標準 BIP-39/32/44 —— 一組助記詞管理所有帳戶，可導入 MetaMask 等主流錢包。',
      'wallet.hdNew': '建立 HD 錢包（標準助記詞）', 'wallet.hdPath': '路徑', 'wallet.hdRestore': '匯入 HD 錢包（助記詞）',
      'wallet.hdRestorePh': '貼上 12/15/18/21/24 詞助記詞', 'wallet.hdDerive': '派生',
      'wallet.hdSetActive': '設定為我的錢包', 'wallet.hdActive': '目前錢包',
      'wallet.advanced': '進階功能（多簽合約錢包）',
      'wallet.addOwner': '新增 Owner', 'wallet.removeOwner': '移除 Owner',
      'wallet.thresholdSet': '設定門檻', 'wallet.setThreshold': '設定', 'wallet.fillThreshold': '請輸入 ≥1 的門檻',
      'wallet.submitTitle': '提交交易（節點代簽）', 'wallet.toPh': '目標地址', 'wallet.valuePh': '金額（TACm）',
      'wallet.submit': '提交交易', 'wallet.fillSubmit': '請填寫目標地址與金額',
      'wallet.approveTitle': '批准交易（附 v/r/s 簽名）', 'wallet.noncePh': 'nonce', 'wallet.approve': '批准交易',
      'wallet.fillApprove': '請填寫 to/value/v/r/s',
      'wallet.queryTitle': '查詢', 'wallet.isownerPh': '檢查地址是否為 owner', 'wallet.query': '查詢',
      'wallet.ownersList': 'Owner 列表', 'wallet.empty': '尚未加入任何 owner',
      'wallet.fillOwner': '請輸入 owner 地址', 'wallet.opOk': '已提交（等待入塊）', 'wallet.opErr': '操作失敗',
      'wallet.isOwnerYes': '是 Owner', 'wallet.isOwnerNo': '非 Owner',
      'wallet.calldataNote': '需自簽名的場景（如 submit）請用 /tacwallet/calldata 取得 calldata，再以私鑰簽名後 POST /tx/submit。',
      'gov.title': '鏈上治理', 'gov.intro': '去中心化 DAO——任何帳戶可提案、一人一票投票、多數決後執行資金轉帳。',
      'gov.status': '合約狀態', 'gov.contract': '合約地址', 'gov.nextPid': '下一個提案 ID',
      'gov.deploy': '部署治理合約', 'gov.proposeTitle': '發起提案', 'gov.targetPh': '目標地址（0x…）', 'gov.valuePh': '金額（TACm）',
      'gov.propose': '發起提案', 'gov.voteTitle': '投票（節點代簽，一人一票）', 'gov.pidPh': '提案 ID',
      'gov.support': '贊成', 'gov.against': '反對', 'gov.vote': '投票',
      'gov.executeTitle': '執行提案（多數決後）', 'gov.execute': '執行',
      'gov.queryTitle': '查詢提案', 'gov.query': '查詢',
      'gov.deployed': '已部署', 'gov.undeployed': '未部署',
      'gov.deployOk': '部署交易已提交', 'gov.deployErr': '部署失敗',
      'gov.fillPropose': '請填寫目標地址與金額', 'gov.fillVote': '請填寫提案 ID 並選擇方向', 'gov.fillPid': '請填寫提案 ID',
      'gov.opOk': '已提交（等待入塊）', 'gov.opErr': '操作失敗',
      'gov.creator': '提案人', 'gov.passed': '通過', 'gov.executed': '已執行', 'gov.pending': '進行中',
      'gov.propInfo': '提案資訊', 'gov.infoEmpty': '尚未建立提案或查無此 ID',
      'gov.daoTitle': '鏈上治理（DAO）', 'gov.paramTab': '參數治理', 'gov.bridgeTitle': '跨鏈橋治理（Kill-switch）', 'gov.bridgeState': '狀態', 'gov.bridgePause': '緊急暫停橋', 'gov.bridgeResume': '恢復橋', 'gov.bridgeNote': '僅限已登入管理員；暫停將拒絕所有跨鏈操作。', 'gov.bridgeNeedLogin': '需先登入主網錢包（管理員）', 'gov.bridgeFail': '操作失敗', 'gov.vaultTitle': 'DeFi 金庫參數治理', 'gov.vaultId': 'Vault ID', 'gov.vaultFee': '績效費率（0-1）', 'gov.vaultMin': '最低存款（TACm）', 'gov.vaultStatus': '狀態', 'gov.vaultApply': '套用參數', 'gov.vaultNote': '查詢 GET /api/defi/vaults 取得金庫 ID 清單。', 'gov.vaultNeedId': '請輸入 Vault ID', 'gov.vaultOk': '金庫參數已更新', 'gov.vaultFail': '更新失敗', 'gov.daoTab': '合約 DAO', 'svc.title': '鏈上服務', 'svc.wallet': 'TAC 錢包（資產＋多簽）', 'svc.ns': '命名服務 .tac', 'svc.gov': '鏈上治理（DAO）', 'vld.title': '節點排名 / Validator Ranking', 'vld.colNode': '節點', 'vld.colStake': '總權重（TACm）', 'vld.colDeleg': '委託總額（TACm）', 'vld.colNum': '委託人數', 'vld.colStatus': '狀態', 'vld.loading': '載入中…', 'vld.modalTitle': '驗證人明細', 'vld.pubkey': '公鑰', 'vld.firstSeen': '首次上線', 'vld.lastActive': '最後活躍', 'vld.delegatorNum': '委託人數', 'vld.totalStake': '總權重', 'vld.colNode': '驗證人', 'vld.delegTotal': '委託總額', 'vld.colStatus': '狀態', 'oracle.title': '鏈上預言機（Oracle）', 'oracle.intro': '主流幣即時喂價——DeFi 借貸、衍生品與開發者的可靠價格來源。', 'oracle.feeds': '價格喂價', 'oracle.query': '查詢與覆寫', 'oracle.symbol': '市場', 'oracle.price': '價格（USDT）', 'oracle.chg': '24h', 'oracle.source': '來源', 'oracle.updated': '更新時間', 'oracle.lookupBtn': '查詢', 'oracle.feedHint': '手動覆寫喂價（節點操作）', 'oracle.feedBtn': '覆寫', 'oracle.historyHint': '歷史喂價', 'oracle.historyBtn': '檢視', 'aa.title': '帳戶抽象（Account Abstraction）', 'aa.intro': 'EIP-4337 智能帳戶——以 UserOperation 授權操作，由 Bundler 代付 Gas 並代執行合約呼叫。', 'aa.submit': '提交 UserOperation', 'aa.submitHint': '填入智能帳戶位址與其簽名（ECDSA），Bundler 驗證授權後代付 Gas 並執行目標合約呼叫。', 'aa.senderPh': 'Sender（智能帳戶位址）', 'aa.pubkeyPh': 'Pubkey（壓縮公鑰 hex）', 'aa.sigPh': 'Signature（DER 簽名 hex）', 'aa.targetPh': 'Target（目標合約位址）', 'aa.callDataPh': 'Call Data（hex，如 00）', 'aa.estimateBtn': '估算 Gas', 'aa.submitBtn': '提交操作', 'aa.estGas': '估算 Gas', 'aa.estFee': '估算費用', 'aa.status': '操作狀態', 'aa.statusBtn': '查詢', 'aa.colStatus': '狀態', 'aa.colTarget': '目標合約', 'aa.colTx': '交易哈希', 'aa.colGas': 'Gas 費用', 'aa.colTime': '提交時間', 'aa.how': '運作方式', 'aa.how1': '持有者用智能帳戶私鑰對操作摘要（含 sender/nonce/target/callData/fee）簽名——摘要由節點 Canonical＋DoubleSHA256 計算。', 'aa.how2': '節點 Bundler（離線 EntryPoint）驗證簽名與 pubkey↔sender 匹配、callData 為合法 hex、gas 參數合理。', 'aa.how3': '驗證通過後由節點金鑰代付 Gas 並提交合約呼叫交易（memo=vm:call:），操作可於 Explorer 追蹤。', 'aa.how4': 'v2 路線：鏈上 EntryPoint 合約＋Paymaster 機制（由任意帳戶代付 Gas）。', 'aa.estimated': '已估算', 'aa.estimating': '估算中…', 'aa.submitting': '提交中…', 'aa.rpcDown': 'RPC 服務不可用', 'aa.failed': '操作失敗', 'aa.txPrefix': '交易：', 'aa.bundlerLabel': 'Bundler', 'aa.fillRequired': '請填寫 sender、target、callData、pubkey 與簽名', 'aa.fillSender': '請輸入 sender 位址', 'aa.opsEmpty': '尚無操作紀錄', 'aa.noncePh': 'Nonce', 'aa.callGasPh': 'Call Gas Limit', 'aa.verifGasPh': 'Verification Gas', 'aa.maxFeePh': 'Max Fee Per Gas', 'nav.aa': 'AA', 'pill.aa': '帳戶抽象', 'api.title': 'Indexer API', 'api.intro': '公共只讀查詢面——區塊、交易、地址與鏈統計，供開發者與工具整合。全部端點回傳 JSON。', 'api.endpoints': '端點 / Endpoints', 'api.base': 'Base URL：', 'api.statsDesc': '鏈統計：高度、區塊數、交易數、帳戶數、Mempool 大小。', 'api.blocksDesc': '區塊列表（高度降序）。limit 預設 20、上限 100。', 'api.blockDesc': '單一區塊（高度或區塊哈希）＋區塊內交易。', 'api.txDesc': '單一交易（64 位 hex 哈希）。', 'api.txsDesc': '地址交易歷史。dir 可選 in / out（僅收/僅發）。', 'api.addrDesc': '地址帳戶：餘額、Nonce、首末活躍時間與交易總數。', 'api.delegDesc': '質押委託分頁：全鏈委託紀錄（委託額降序，含委託人／節點／金額／時間）。', 'api.delegatorDesc': '委託人視角：全部委託明細＋累計委託獎勵（coinbase:deleg 鏈上入賬）。', 'api.validatorsDesc': '節點排名：權重（自有＋委託）／委託總額／委託人數／狀態，權重降序。', 'api.usage': '使用規範 / Usage', 'api.usage1': '全部端點為只讀，不消耗 Gas、不需簽名。', 'api.usage2': '分頁上限：limit ≤ 100。', 'api.usage3': '錯誤回傳：404（不存在）／400（參數/地址/哈希格式錯誤）／500（內部錯誤）。', 'api.usage4': '與 eth_* JSON-RPC 並存：RPC 面供錢包/合約，Indexer 面供瀏覽器與分析工具。', 'st.title': 'PoT 質押委託（Proof-of-Trust Staking）', 'st.intro': '將 TACm 委託給節點——委託增加節點出塊權重（total_stake），紀錄隨區塊上鏈、全網一致。委託後資產鎖倉，撤銷即返還。', 'st.delegateTitle': '委託 / 撤銷', 'st.delegateHint': '填入你的錢包地址、目標節點（節點）地址與金額。委託後站內帳本鎖倉並上鏈紀錄；撤銷上鏈後返還。', 'st.fromPh': 'From（你的 tx0 地址）', 'st.validatorPh': '節點地址', 'st.amountPh': 'Amount（TACm）', 'st.delegateBtn': '委託 Delegate', 'st.undelegateBtn': '撤銷 Undelegate', 'st.myTitle': '我的委託', 'st.queryBtn': '查詢', 'st.colValidator': '節點', 'st.colAmount': '委託額（TACm）', 'st.colTime': '時間', 'st.validatorsTitle': '節點委託總額', 'st.colTotal': '委託總額（TACm）', 'st.colCount': '委託人數', 'st.rewardsTitle': '累計委託獎勵（鏈上 coinbase:deleg）', 'st.unbondTitle': '解綁中（Unbonding）', 'st.colComplete': '完成高度', 'st.colStatus': '狀態', 'st.colVStatus': '狀態',      'mining.mech4': '・節點自身維護一台默認礦機（不參與瓜分）；若鏈上暫無在線礦工，該塊 40% 礦工份額全數挹注獎勵池，離線期間不會獲得任何收益。',
    'mining.earnTitle': '我的收益明細（近 7 日 · 鏈上 coinbase 瓜分）', 'mining.earnSub': '每日收益（TACm，綠）／每日平均時權（藍，右軸）',
    'mining.earnDate': '日期', 'mining.earnAmt': '收益 TACm', 'mining.earnCnt': '瓜分筆數', 'mining.earnHash': '平均時權',
    'mining.twTitle': '時權設定（固定不可改）', 'mining.twDesc1': '基礎時間權證：節點/礦機在線即取得（預設 1.0）', 'mining.twDesc2': '加成證明權證：鏈上證明（質押／推薦註冊）累加（預設 2.0）', 'mining.twDesc3': '邀請碼＝你的錢包地址，分享給朋友於註冊時填寫即可。',
      'wallet.title': 'TAC 錢包', 'wallet.ledger': '最近帳本流（審計分錄）', 'wallet.transfer': '站內轉帳', 'wallet.confirm': '確認轉帳',
      'dash.title': '儀表板', 'chain.mined': '全鏈總產出 TACm', 'chain.tiusd': 'TiUSD 總流通',
      'chain.pool': '獎勵池', 'chain.earned': '我的累計收益', 'chain.rate': '出塊速率',
      'index.title': 'TAC Ai 智能鏈', 'index.overview': '全鏈儀表',
      'bn.wallet': 'Web3 錢包', 'bn.walletSub': '鏈上資產與轉帳',
      'bn.mining': '開始挖礦', 'bn.miningSub': '算力與獎勵',
      'bn.exchange': '交易所', 'ss.title': '穩定幣兌換（TiUSD ↔ USDT）', 'ss.sub': '1:1 錨定兌換 · 0.5% 手續費入庫 · 治理可調', 'ss.addr': 'address (tx0...)', 'ss.from': '從', 'ss.to': '到', 'ss.btn': '兌換', 'ss.note': '提示：TiUSD 為 TACm 鏈穩定幣（peg 1 USDT）。USDT 可經多鏈橋入金；TiUSD 可經 DeFi CDP 抵押 TACm 鑄造。', 'ss.req': '請輸入地址與金額', 'ss.ok': '兌換成功', 'bn.exchangeSub': '閃兌與訂單',
      'ex.hot': '熱門市場', 'ex.tapTrade': '點選幣種即可交易', 'ex.ref': '參考',
      'ex.onchain': '鏈上', 'ex.live': '即時', 'ex.refDisabled': '已停用：僅鏈上資產（TACM/TiUSD/USDT）可交易', 'ex.fee': '手續費',
      'ex.verifyBtn': '驗證地址', 'ex.verifyUn': '未驗證', 'ex.verifyOK': '地址已驗證：', 'ex.verifyNeedLogin': '請先登入會員以驗證綁定的礦機錢包地址', 'ex.verifyFail': '驗證失敗，請稍後再試', 'ex.verifyFirst': '請先點「驗證地址」綁定當前礦機錢包', 'ex.verifyChanged': '帳戶地址已變更，請重新驗證',
      'ex.walletTitle': '交易所錢包', 'ex.walletNone': '未連結，請先連結錢包', 'ex.walletMiner': '連結礦工錢包', 'ex.walletNew': '新建 Web3 錢包', 'ex.walletChange': '更換', 'ex.walletMinerOK': '已連結礦工錢包（同當前鏈）：', 'ex.walletNeedLogin': '請先登入會員，才能連結礦工錢包', 'ex.walletNewConfirm': '將為你建立一組全新 Web3 錢包，建立後請立即備份私鑰。是否繼續？', 'ex.walletNewOK': '已建立全新 Web3 錢包地址：', 'ex.walletWifLabel': '私鑰 WIF（請立即離線備份，遺失無法找回）：', 'ex.walletWifWarn': '請立即抄寫並離線保存私鑰，切勿外洩；換裝置時需用私鑰恢復。', 'ex.walletNewFail': '新建 Web3 錢包失敗，請稍後再試', 'ex.walletChangeConfirm': '確定要更換交易所錢包嗎？目前欄位將清空。', 'ex.orderMarket': '交易對',
      'ex.refBook': '參考行情（估值對映）· 無訂單簿',
      'ex.refDepth': '主流幣估值行情 · 無真實訂單簿',
      'ex.best': '最優',
      'ex.loading': '載入中…',
      'ex.liveBook': '即時行情 · 無訂單簿',
      'ex.refSwap': '估值兌換 · 手續費', 'ex.tradeOk': '兌換成功',
      'bn.community': '社群', 'bn.communitySub': '動態與獎勵',
      'bn.live': '直播', 'bn.liveSub': '社群直播 · 開播即時互動',
      'cm.brand': 'TAC 社群', 'cm.home': '首頁', 'cm.video': '影片', 'cm.friends': '好友',
      'cm.market': '市集', 'cm.notify': '通知', 'cm.me': '個人',
      'cm.back': '返回', 'cm.switch': '切換', 'cm.profile': '個人主頁', 'cm.myFriends': '你的朋友',
      'cm.followersYou': '追蹤你的人', 'cm.suggest': '建議（可能認識）', 'cm.online': '人在線',
      'cm.myWallet': '我的錢包', 'cm.myMiner': '我的礦機', 'cm.exchange': '交易所', 'cm.myPosts': '我的貼文',
      'cm.chainAssets': '鏈上資產', 'cm.編輯': '編輯', 'cm.刪除': '刪除', 'cm.儲存': '儲存', 'cm.取消': '取消', 'cm.載入中': '載入中…',
      'cm.未命名': '未命名', 'cm.對外顯示名稱': '對外顯示名稱', 'cm.個人頭像': '個人頭像', 'cm.已儲存': '已儲存', 'cm.已更新': '已更新',
      'cm.已刪除': '已刪除', 'cm.編輯影片描述': '編輯影片描述：', 'cm.確定刪除這則貼文': '確定刪除這則貼文？', 'cm.確定刪除這支影片': '確定刪除這支影片？',
      'cm.編輯貼文內容': '編輯貼文內容：', 'cm.編輯個人資料': '編輯個人資料', 'cm.儲存失敗': '儲存失敗', 'cm.編輯失敗': '編輯失敗', 'cm.刪除失敗': '刪除失敗',
      'cm.namePh': '輸入顯示名稱…', 'cm.avaPh': 'https://… 或 /static/…',
      'cm.noVideo': '尚未有影片，快來發佈第一支影片吧！',
      'cm.postsN': '則貼文', 'cm.following': '追蹤中', 'cm.followers': '追蹤者', 'cm.videoTag': '影片', 'cm.marketTag': '市集', 'cm.openVideo': '開啟影片',
      'cm.sell': '發布商品', 'cm.listItem': '上架', 'cm.buyTitle': '確認購買', 'cm.confirmPay': '確認付款（一鍵轉帳）',
      'cm.rateItem': '評價市集商品', 'cm.rating': '評分（1-5）', 'cm.submit': '送出評價',
      'cm.adCreate': '建立廣告', 'cm.adPlace': '投放位置', 'cm.adPlaceRule': '資訊流插播',
      'cm.adBudget': '總預算', 'cm.adDaily': '每日預算', 'cm.adTarget': '點擊跳轉網址', 'cm.adTargetRule': '按「了解更多」前往',
      'cm.adSubmit': '發佈廣告（付款後投放）', 'cm.myAds': '我的廣告', 'cm.了解更多': '了解更多',
      'cm.已售出': '已售出', 'cm.購買': '購買', 'cm.評價': '評價', 'cm.賣家': '賣家', 'cm.鏈上費': '鏈上費',
      'cm.合計': '合計', 'cm.處理中': '處理中…', 'cm.商品 ID': '商品 ID',
      'cm.市集尚無商品': '市集尚無商品', 'cm.載入失敗': '載入失敗', 'cm.請先輸入你的錢包地址': '請先輸入你的錢包地址',
      'cm.addrFromWallet': '以錢包中心為準', 'cm.walletNeedWalletCenter': '請先於錢包中心選擇錢包地址（tx0 開頭）', 'cm.walletNeedWalletCenterShort': '請先於錢包中心選擇錢包地址',
      'cm.請填寫商品名稱與價格': '請填寫商品名稱與價格', 'cm.上架失敗': '上架失敗：',
      'cm.廣告已建立': '廣告已建立（ID #', 'cm.請填寫廣告標題與總預算': '請填寫廣告標題與總預算', 'cm.發佈失敗': '發佈失敗：',
      'cm.投放中': '投放中', 'cm.待付款': '待付款', 'cm.付款投放': '付款投放',
      'cm.動態流': '動態流', 'cm.影片流': '影片流', 'cm.動態流+影片流': '動態流＋影片流',
      'cm.總預算': '總預算', 'cm.每日': '每日', 'cm.曝光': '曝光', 'cm.點擊': '點擊', 'cm.尚無廣告': '尚無廣告',
      'cm.確認支付廣告預算並開始投放？': '確認支付廣告預算並開始投放？', 'cm.廣告已投放': '廣告已投放（ID #',
      'cm.付款失敗': '付款失敗：', 'cm.請填寫廣告標題與總預算': '請填寫廣告標題與總預算',
      'cm.login': '登入', 'cm.live': '直播', 'cm.newPost': '建立貼文', 'cm.upload': '上傳圖片/影片',
      'cm.kPost': '動態', 'cm.kVideo': '影片', 'cm.kLive': '直播', 'cm.kMarket': '市集商品', 'cm.post': '發布',
      'bn.dex': 'DEX', 'bn.dexSub': '流動性與兌換',
      'bn.bridge': '橋', 'bn.bridgeSub': '跨鏈資產互通（TACm ↔ BSC）',
      'br.status': '中繼狀態', 'br.assets': '資產映射', 'br.lock': '鎖定至',
      'br.asset': '資產', 'br.srcAddr': '來源地址（tx0）', 'br.dstAddr': '目標地址（0x BSC）', 'br.amount': '金額',
      'br.lockBtn': '鎖定並發送', 'br.history': '跨鏈歷史', 'br.historyHint': '依地址',
      'br.query': '查詢', 'br.historyPh': '輸入地址查看跨鏈交易',
      'bn.nfts': 'NFT 收藏', 'bn.nftsSub': '數位收藏',
      'bn.ai': 'Titan Ai', 'bn.aiSub': '鏈上智能助手 · 單一 AI 入口',
      'bn.explorer': '區塊瀏覽', 'bn.explorerSub': '鏈上搜尋',
      'index.heroTag': 'AI 原生區塊鏈 · EVM 相容 · EIP-1559 · L2 Rollup · BSC 跨鏈橋',
      'feat.wallet': '資產與轉帳', 'feat.mining': '算力與獎勵', 'feat.explorer': '鏈上搜尋',
      'feat.exchange': '交易所', 'feat.community': '社群', 'feat.dex': 'DEX', 'feat.nfts': 'NFT', 'feat.ai': 'Titan Ai',
      'index.v1t': '你的資產屬於你', 'index.v1s': '你的銀行帳戶只是別人資料庫裡的一筆記錄。TAC 是另一個網絡——你直接持有資產，不經第三方。',
      'index.v2t': '隱私至上', 'index.v2s': '沒有與你名字綁定的帳戶、沒有公司監看你的餘額。你的鏈上活動由你掌控。',
      'index.v3t': '永不離線', 'index.v3s': '多節點去中心化網絡，100% 正常運行。沒有單一伺服器可以關閉你的鏈。',
      'index.v4t': 'AI 原生', 'index.v4s': '鏈上 AI 助手＋社群獎勵。TAC 是為智能時代打造的自治網絡。',
      'index.ctaWallet': '進入錢包', 'index.ctaMining': '開始挖礦', 'index.ctaJoin': '加入網絡', 'index.ctaExplorer': '區塊瀏覽',
      'index.latestTx': '最新交易',       'ld.tag': '一個為智能時代打造的自治網絡：PoT＋PoS＋BFT 共識、EVM 相容、EIP-1559、L2 Rollup、BSC 跨鏈橋、鏈上 AI——你的資產、你的社群、你的未來，都在鏈上。',
      'ld.enter': '進入系統', 'ld.wallet': '開啟錢包', 'ld.mining': '開始挖礦', 'ld.explorer': '區塊瀏覽',
      'ld.sec1t': '為什麼選擇 TAC Ai 智能鏈？', 'ld.sec1s': '不是另一個中心化伺服器——是一個真正屬於用戶的網絡。以下是六大優勢。',
      'ld.d1t': '真正的去中心化', 'ld.d1s': '採用 PoT＋PoS＋BFT 三層共識與時間權證動態難度，任何節點都可加入出塊與驗證。沒有單一公司、沒有單一伺服器可以關閉或竄改這條鏈。',
      'ld.d2t': '密碼學級安全', 'ld.d2s': 'ECDSA 簽名、SHA-256 鏈式哈希、SPV 最終性與 Merkle 驗證層層把關；內建全鏈賬本重放審計，每 100 塊自動核對 coinbase／分潤，不一致即時標記。',
      'ld.d3t': '資產屬於你', 'ld.d3s': '沒有綁定姓名的帳戶。錢包私鑰由你保管，鏈上資產直接由你控制；內建交易所與跨鏈橋讓資產自由閃兌、轉入轉出。',
      'ld.d4t': '完整生態', 'ld.d4s': '內建錢包、交易所、DEX、DeFi、IDO、NFT、Token Studio、社群市集、鏈上治理——一個帳號從發幣到交易到治理全閉環。',
      'ld.d5t': '與主流鏈互通', 'ld.d5s': 'EVM 相容＋eth_* JSON-RPC：MetaMask 直接接入、Type-2（EIP-1559）交易支援；BSC 真實跨鏈橋讓資產在主網之間平滑轉移。',
      'ld.d6t': 'AI 原生', 'ld.d6s': '鏈上 AI 助手直接查詢餘額、交易、出塊與治理參數；社群獎勵自動上鏈。TAC 天生為智能時代而設計。',
      'ld.specT': '主網技術規格', 'ld.enterT': '從這裡開始','index.height': '鏈頂高度', 'index.final': '最終化高度', 'index.diff': '當前難度', 'index.mempool': '待打包交易',
      'auth.register': '註冊', 'auth.login': '登入', 'auth.email': 'Email', 'auth.password': '密碼',
      'tokens.title': '代幣工作室', 'tokens.sub': '鏈上標準代幣（ERC-20）· 節點官方發行 · 即時出塊',
      'tokens.count': '個代幣', 'tokens.deploy': '發行代幣', 'tokens.name': '名稱（≤32 字元）', 'tokens.symbol': '代號（≤32 字元）',
      'tokens.supply': '總供應量', 'tokens.from': '發行者地址（選填；發行費 100 TACm 由此扣，空則節點代付）', 'tokens.usage': '發行用途：可在 DEX 建立流動性池交易（AMM）、ERC-20 轉帳/授權/跨鏈、IDO 認購、社群市集付款。', 'tokens.fee': '發行費', 'tokens.deploy_btn': '⚡ 發行', 'tokens.detail': '代幣詳情', 'tokens.list': '已發行代幣',
      'tokens.select': '← 從列表選擇代幣', 'tokens.mybal': '我的餘額', 'tokens.contract': '合約地址',
      'tokens.to': '轉帳至（tx0 地址）', 'tokens.amount': '數量', 'tokens.transfer': '⇄ 代簽轉帳',
      'nfts.title': 'NFT 工作室', 'nfts.sub': '鏈上標準 NFT（ERC-721）· 節點官方發行 · 即時出塊',
      'nfts.count': '個 NFT 集合', 'nfts.deploy': '發行 NFT 集合', 'nfts.deploy_btn': '⚡ 發行',
      'nfts.detail': 'NFT 詳情', 'nfts.list': '已發行 NFT 集合', 'nfts.mybal': '我的持有數',
      'nfts.contract': '合約地址', 'nfts.mint_to': '鑄造給（tx0 地址）', 'nfts.token_id': 'Token ID',
      'nfts.mint': '⚡ 代簽鑄造', 'nfts.owner_query': '查詢 Owner', 'nfts.owner_of': 'Token Owner',
      'nfts.token_uri': 'Token URI', 'nfts.transfer_from': '轉移 From（tx0）', 'nfts.transfer_to': '轉移 To（tx0）',
      'nfts.transfer_btn': '⇄ 代簽轉移', 'nfts.supply': '已鑄造數量', 'nfts.uri_placeholder': 'e.g. 1（數字）',
      'nfts.hint_owner': '需由 NFT 所有者本人或授權者轉移（節點代簽=節點帳戶）',
      'nfts.approve_zone': '授權管理（ERC-721 Approval）', 'nfts.approve_to': '授權給（tx0 地址）',
      'nfts.approve_btn': '⚡ 代簽授權', 'nfts.get_approved': '查詢 getApproved', 'nfts.approved_of': 'Token 獲批者',
      'nfts.operator': '操作員（tx0）', 'nfts.owner_addr': 'Owner（tx0）', 'nfts.set_all_true': '⇄ 全量授權（true）', 'nfts.set_all_false': '⇄ 撤銷（false）',
      'nfts.operator_approved': '查詢 isApprovedForAll', 'nfts.approve_hint': 'approve 授權單一 Token 給指定地址；setApprovalForAll 授權操作員代管全部 Token（節點代簽=節點帳戶為 owner/授權人）',
      'nav.dex': 'DEX', 'nav.orderbook': '訂單簿', 'nav.aiv': 'AI 驗證', 'pill.dex': 'DEX', 'pill.orderbook': '訂單簿', 'pill.aiv': 'AI 驗證', 'pill.sign': '簽名工具',
      'nav.dexv3': 'DEX V3', 'nav.perp': '永續合約', 'nav.aimarket': 'AI 模型市場', 'pill.dexv3': 'DEX V3', 'pill.perp': '永續合約', 'pill.aimarket': 'AI 市場',
      'ex.uidHint': '帳戶地址＝登入帳號綁定地址（同主網新鏈）；未登入訪客地址每次瀏覽器可能不同，請登入後再下單。',
      'ex.back': '返回交易所', 'ex.ecoTitle': '進階交易與生態', 'ex.ecoHint': '統一錢包地址 · 與現貨共用',
      'ex.dex': 'DEX', 'ex.dexS': 'AMM 流動池 · 即時兌換', 'ex.dexv3': 'DEX V3', 'ex.dexv3S': '集中流動性 · 主動做市',
      'ex.perp': '永續合約', 'ex.perpS': '槓桿多空 · 資金費率', 'ex.aimarket': 'AI 模型市場', 'ex.aimarketS': '部署與呼叫鏈上模型',
      'ex.aiverify': 'AI 驗證', 'ex.aiverifyS': '跨鏈推理結果驗證',
      'sign.title': '簽名工具台', 'sign.sub': '開發者簽名工具 — EIP-191 personal_sign · EIP-712 typed data · ecRecover',
      'contract.title': '合約驗證中心', 'contract.sub': '檢視已部署合約並以 ABI 編碼執行只讀互動（不寫鏈）',
      'contract.listTitle': '已部署合約', 'contract.thAddr': '地址', 'contract.thName': '名稱', 'contract.thSymbol': '代號', 'contract.thSupply': '供應量', 'contract.thCode': '代碼',
      'contract.loading': '載入中…', 'contract.readTitle': '合約互動', 'contract.readSub': 'ABI 編碼 eth_call · 不改變鏈狀態', 'contract.reload': '重新載入',
      'contract.read': '讀取', 'contract.copy': '複製', 'contract.infoTitle': '合約詳情', 'contract.infoSub': '代碼 · 存儲 · 餘額', 'pill.contracts': '合約驗證',
      'contract.verifyTitle': '驗證合約', 'contract.verifySub': '附上 ABI · 啟用一鍵方法表單', 'contract.verify': '驗證', 'contract.loadAbi': '載入 ABI',
      'sign.account': '節點帳戶', 'sign.addr': '節點地址', 'sign.chain': '鏈 ID',
      'sign.ps': 'Personal Sign（EIP-191）', 'sign.psSub': '節點金鑰 · 0x65 位元組簽名',
      'sign.msg': '訊息', 'sign.utf8': 'UTF-8 文字', 'sign.hex': 'Hex（0x…）', 'sign.sign': '簽名', 'sign.sig': '簽名結果', 'sign.copy': '複製',
      'sign.verify': '恢復簽名者（personal_ecRecover）', 'sign.recover': '恢復', 'sign.recovered': '恢復的簽名者',
      'sign.match': '符合節點帳戶', 'sign.mismatch': '與節點帳戶不符',
      'sign.td': 'Typed Data v4（EIP-712）', 'sign.tdSub': '已預填官方 Mail 範例', 'sign.tdJson': 'Typed data JSON', 'sign.tdSign': '簽署 typed data',
      'sign.legacy': 'eth_sign（舊式）＋ eth_ecRecover', 'sign.legacySub': 'keccak256(message) — 僅相容用途',
      'sign.err': '錯誤', 'sign.wait': '處理中…',
      'dex.title': '鏈上 DEX（AMM）', 'dex.sub': '恆定乘積做市 · 鏈上結算 · 0.3% 手續費',
      'dex.pools_word': '個池', 'dex.fee': '手續費 0.30%',
      'dex.pools': '流動性池', 'dex.pools_hint': '發行代幣後可建池交易', 'dex.empty': '尚無流動性池',
      'dex.create': '建立流動性池', 'dex.token0': '代幣 0', 'dex.token1': '代幣 1', 'dex.create_btn': '建立池',
      'dex.liquidity': '流動性', 'dex.pool': '池', 'dex.amount0': '數量（代幣 0）', 'dex.amount1': '數量（代幣 1）',
      'dex.add_btn': '＋ 注入流動性', 'dex.mypos': '我的 LP 份額', 'dex.remove_btn': '－ 退出全部流動性',
      'dex.swap': '即時兌換', 'dex.swap_hint': '恆定乘積自動滑價',
      'dex.token_in': '輸入代幣', 'dex.amount_in': '輸入數量', 'dex.swap_btn': '⇄ 兌換',
      'dex.slippage': '滑點容忍度', 'dex.slippage_custom': '自訂滑點（%）',
      'dex.il_title': '無常損失風險', 'dex.il_body': '提供流動性後，若代幣價格大幅波動，退出時價值可能低於單純持有（IL＝價格波動損失，與手續費收益互相抵消）。高波動交易對風險更大。', 'dex.il_ack': '我已了解並接受無常損失風險',
      'dex.audit': '安全審計', 'dex.audit_hint': '鏈上可驗證',
      'dex.audit1': '核心鏈一致性（PoS/PoT coinbase）', 'dex.audit2': 'EVM 原子性與 gas 治理', 'dex.audit3': '跨鏈橋多簽＋SPV', 'dex.audit4': '全鏈帳本重放（ledger audit）', 'dex.audit5': '第三方機構級審計', 'dex.audit_note': '審計方法可鏈上複驗：瀏覽器交易明細、ledger audit API、跨鏈橋事件日誌皆為公開資料。',
      'dex.farm': '流動性挖礦（Farm）', 'dex.farm_hint': '質押 LP 份額賺取獎勵代幣', 'dex.farm_empty': '尚無農場：先建立流動性池並注資獎勵',
      'dex.farm_stake': '質押', 'dex.farm_unstake': '解除質押', 'dex.farm_claim': '領取獎勵',
      'gov.title': '鏈上治理', 'gov.sub': '鏈上提案 · 權重投票 · 自動執行',
      'gov.proposals': '全部提案', 'gov.propose': '發起提案', 'gov.rules': '治理規則',
      'gov.params': '可治理參數',
      'gov.window': '投票窗口', 'gov.threshold': '通過門檻', 'gov.quorum': '法定人數', 'gov.timelock': '時間鎖', 'gov.create': '提交提案',
      'gov.title_f': '標題', 'gov.desc': '描述', 'gov.type': '類型', 'gov.param': '參數',
      'gov.value': '數值', 'gov.address': '你的地址', 'gov.amount': '金額 (TACm)', 'gov.none': '尚無提案', 'gov.myRole': '我的角色', 'gov.adminYes': '管理員 ✓', 'gov.adminNo': '一般會員', 'gov.adminGuest': '未登入（僅管理員可操作橋）', 'gov.adminNote': '管理員可升級/降級其他帳號（僅限已具管理員角色者）。', 'gov.adminBtn': '套用', 'gov.adminOk': '已更新管理員角色', 'gov.adminFail': '設定失敗，請稍後再試', 'gov.adminNeedEmail': '請輸入 email',
      'gov.tp_param': '參數調整', 'gov.tp_treasury': '國庫支出', 'gov.tp_meta': '治理決議',
      'gov.pk_bt': '出塊時間 (block_time)', 'gov.pk_d': '基礎難度 (difficulty)',
      'gov.pk_f': '手續費基準 (tx_fee_bps)', 'gov.pk_t': 'TiUSD 下限 (tiusd_floor_bps)', 'gov.pk_q': '法定人數 (gov_quorum_pct)', 'gov.pk_tl': '時間鎖 (gov_timelock_blocks)', 'gov.pk_pm': '提案門檻 (gov_proposer_min)', 'gov.pk_ts': '權重總供應 (gov_total_supply)',
      'common.copy': '複製', 'common.refresh': '刷新', 'common.close': '關閉', 'common.confirm': '確認', 'common.loading': '載入中…',
      'net.audit': ' 鏈上審計（區塊 · 交易 · 合約）', 'net.alertsTitle': '供應警報（TiUSD）',
      'net.auditTip': '正在驗證最近 100 塊＋全鏈賬本重放…',
      'net.auditNA': '審計暫不可用', 'net.auditNA2': '無法驗證',
      'net.auditInc': '增量審計（自區塊', 'net.auditFull': '全量審計',
      'net.auditBlocks': '審計塊數', 'net.auditCoin': 'coinbase==獎勵', 'net.auditPool': 'pool 15%',
      'net.auditSplit': 'node 45% · miner 40%', 'net.auditMined': '全鏈總產出', 'net.auditPoolBal': '獎勵池餘額',
      'net.auditPoolShare': 'Pool 挹注（鏈上）', 'net.auditMiners': '鏈上在線礦工',
      'net.txChecked': '交易筆數（重放）', 'net.txSig': '簽名 / nonce / 餘額', 'net.txContract': '合約交易', 'net.ledger': '賬本守恆',
      'net.bad': ' 異常',
      'net.good': ' 正常',
      'net.ledgerOK': '全鏈賬本一致', 'net.ledgerOKDesc': '每筆交易簽名/nonce/餘額全過，合約格式合法，總餘額 == coinbase 增發',
      'net.ledgerBAD': '賬本異常', 'net.ledgerBADDesc': '交易/合約層發現異常，需立即檢查',
      'net.onchainOK': '鏈上一致', 'net.onchainOKDesc': '每塊 coinbase 總額嚴格等於出塊獎勵（節點 45% / pool 15% / miner 40%），無超發/少發，供應與獎勵代表鏈上真實',
      'net.onchainBAD': '發現異常', 'net.onchainBADDesc': '鏈上獎勵不一致，需立即檢查',
      'net.issueList': '異常清單：',
      'node.badge': 'NODE', 'node.title': '節點品牌控制台', 'node.h1': '節點獨立品牌前端',
      'node.p1': '節點已獨立成自有品牌（TAC Ai Smart Chain — Node），檔案自成一個資料夾（cmd/nodeweb + internal/nodeui），與主網用戶功能完全分離。',
      'node.encNote': '🔒 P2P 通訊 v65 起預設加密（ECDH+AES-GCM），對端為舊版節點時自動降級明文；強制明文加 -p2p-plain。節點儀表 Peers 顯示 🔒加密／⚠明文。',
      'node.acc1': '節點儀表', 'node.acc2': '同步狀態', 'node.acc3': 'P2P Peers', 'node.acc4': '節點', 'node.acc5': '出塊記錄',
      'node.cmdTitle': '啟動指令', 'node.cmdHint': '以下指令一次一條，於節點伺服器執行：',
      'node.seedTitle': '種子節點', 'node.step1': '下載套件並解壓。', 'node.step2': '用同一創世節點啟動（跟隨模式）。', 'node.step3': '開啟節點品牌控制台。', 'node.step4': '節點自動同步、驗證並由在線節點均分 45% 節點獎勵。',
      'node.dlTitle': '下載節點程式（免編譯）', 'node.dlDesc': '官方預編譯純節點程式，下載即用（不需 Go、不需編譯）：', 'node.dlNote': '架設者依此下載、安裝並以 systemd 常駐；節點金鑰自動生成＝你的收款地址，請備份 node_key.json。',
      'ld2.navEco': '生態系統', 'ld2.navWhy': '為什麼 TAC', 'ld2.navHow': '使用方式', 'ld2.navSpec': '技術規格', 'ld2.navJoin': '加入網絡',
      'ld2.enter': '進入系統', 'ld2.tag2': '為智能時代打造的一體化區塊鏈——共識、EVM、L2、跨鏈橋與 AI 全在同一條鏈上。你的資產、你的社群、你的未來，都在鏈上。',
      'ld2.cta1': '立即開始', 'ld2.cta2': '開始挖礦', 'ld2.trust2': '✓ 鏈上審計一致', 'ld2.trust3': '✓ 完整生態',
      'ld2.online': '在線礦工', 'ld2.ecoT': '完整的鏈上生態', 'ld2.ecoS': '錢包 · 交易所 · 挖礦 · DeFi · NFT · 治理',
      'ld2.svcT': '探索生態系統', 'ld2.svcS': '一切都在同一條鏈上——一個帳號、一套帳本、全閉環。',
      'ld2.go': '前往 →', 'ld2.s1t': '錢包', 'ld2.s1s': '多資產帳戶，完整鏈上紀錄',
      'ld2.s2t': '挖礦', 'ld2.s2s': '算力瓜分，即時獎勵入帳',
      'ld2.s3t': '交易所', 'ld2.s3s': '訂單簿＋閃兌＋鏈上交易',
      'ld2.s4t': 'DEX', 'ld2.s4s': 'AMM 流動性池，即時兌換',
      'ld2.s5t': 'DeFi', 'ld2.s5s': '流動性挖礦＋借貸＋Vault',
      'ld2.s6t': 'NFT',
      'ld2.s7t': '流動質押',
      'ld2.s7s': 'stTACm 流動憑證，獎勵自動注入',
      'ld2.s8t': '網路健康',
      'ld2.s8s': '節點狀態與心跳儀表', 'ld2.s6s': 'ERC-721 收藏與交易',
      'ld2.howT': '三步開始', 'ld2.howS': '從錢包到治理，全部鏈上完成。',
      'ld2.h1t': '建立錢包', 'ld2.h1s': '一鍵生成地址與私鑰——你的資產、你的金鑰。',
      'ld2.h2t': '選擇服務', 'ld2.h2s': '挖礦、交易、DeFi、NFT——進入任何一個生態入口。',
      'ld2.h3t': '參與治理', 'ld2.h3s': '發起提案、權重投票、參數自動執行。',
      'ld2.ctaT': '準備好上鏈了嗎？', 'ld2.ctaS': '加入主網——挖礦、交易或部署你的合約。',
      'ld2.f1': '快速連結', 'ld2.f2': '網絡', 'ld2.cr': '© 2026 TAC Ai 智能鏈。保留所有權利。',

    
      'pill.sign': '簽章工具',
      'pill.contracts': '合約驗證',
      'pill.zk': 'ZK／VRF',
      'contract.title': '合約驗證',
      'contract.sub': '瀏覽已部署合約並讀取／驗證',
      'contract.listTitle': '已部署合約',
      'contract.thAddr': '地址',
      'contract.thName': '名稱',
      'contract.thSymbol': '符號',
      'contract.thSupply': '供應量',
      'contract.loading': '載入中…',
      'contract.readTitle': '讀取合約',
      'contract.readSub': 'ABI 編碼 eth_call · 無狀態變更',
      'contract.reload': '重新載入',
      'contract.loadAbi': '載入 ABI',
      'contract.read': '讀取',
      'contract.copy': '複製',
      'contract.verifyTitle': '驗證合約',
      'contract.verifySub': '附加 ABI · 一鍵讀取',
      'contract.verify': '驗證',
      'contract.infoTitle': '合約詳情',
      'contract.infoSub': '程式碼 · 儲存 · 餘額',
      'sign.title': '簽章工具',
      'sign.sub': '開發者簽章工具——訊息簽名與地址恢復',
      'sign.account': '節點帳戶',
      'sign.addr': '節點地址',
      'sign.ps': '個人簽名（EIP-191）',
      'sign.psSub': '節點金鑰 · 0x65 字節簽名',
      'sign.utf8': 'UTF-8 文字',
      'sign.hex': 'Hex（0x…）',
      'sign.sign': '簽名',
      'sign.copy': '複製',
      'sign.verify': '恢復簽署者（personal_ecRecover）',
      'sign.recover': '恢復',
      'sign.td': '型別資料 v4（EIP-712）',
      'sign.tdSub': '官方 Mail 範例預填',
      'sign.tdSign': '簽署型別資料',
      'sign.legacy': 'eth_sign（舊版）＋ eth_ecRecover',
      'sign.legacySub': 'keccak256(message) — 相容性',
      'cm.editProfile': '編輯',
      'cm.dispName': '對外顯示名稱',
      'cm.avatar': '個人頭像（圖片網址或上傳後網址）',
      'cm.cancel': '取消',
      'cm.save': '儲存',
      'tx.status': '狀態',
      'tx.confirmations': '確認數',
      'tx.blocks': '區塊',
      'tx.block': '所在區塊',
      'tx.fee': '手續費',
      'tx.gasPrice': 'Gas Price（有效）',
      'cm.loading': '載入中…',
      'hz.dexv3': 'DEX V3', 'hz.dexv3s': '集中流動性兌換',
      'hz.perp': '永續合約', 'hz.perps': '槓桿多空交易',
      'hz.aimarket': 'AI 模型市場', 'hz.aimarkets': '部署與呼叫模型',
      'hz.orderbook': '訂單簿', 'hz.orderbooks': '掛單深度交易',
      'hz.aiverify': 'AI 驗證', 'hz.aiverifys': '內容真實性證明',
      'hz.oracle': '預言機', 'hz.oracles': '鏈上價格餵送',
      'hz.zk': 'ZK/VRF', 'hz.zks': '零知識與可驗證隨機',
      'hz.faucet': '水龍頭', 'hz.faucets': '測試代幣領取',
      'hz.ens': 'ENS 域名', 'hz.enss': '鏈上域名解析',
      'hz.contracts': '合約驗證', 'hz.contractss': '原始碼驗證',
      'hz.sdk': 'SDK 開發工具', 'hz.sdks': '開發者套件',
      'hz.sign': '簽名工具', 'hz.signs': '訊息簽章',
},
    en: {
      'nav.home': 'Home', 'nav.wallet': 'Wallet',
      'ls.home': 'Home', 'ls.defi': 'DeFi', 'ls.stake': 'Staking', 'ls.liquid': 'Liquid Staking', 'ls.gov': 'Governance', 'ls.apply': 'Apply',
      'ls.heroTitle': 'PoT Liquid Staking (Proof-of-Trust)', 'ls.heroDesc': 'Deposit TACm to mint stTACm liquid receipts — your balance grows with staking rewards via exchange rate. Redeem anytime; use stTACm across DeFi.',
      'ls.tvl': 'Total Staked (TACm)', 'ls.stSupply': 'stTACm Supply', 'ls.rate': 'Exchange Rate', 'ls.apy': 'Est. APY', 'ls.depositors': 'Depositors', 'ls.rateChart': 'Exchange Rate Live Chart (stTACm/TACm)', 'ls.rateNote': 'Sampled every 10s (local history); the rate rises as delegation rewards are injected.',
      'ls.tiusdTitle': 'Getting TiUSD (stablecoin 1:1 pegged to USDT)', 'ls.tiusdDesc': 'TiUSD is not a free faucet asset: ① DeFi over-collateralized CDP mint with TACm collateral (1:1 peg); ② Stable Swap USDT ↔ TiUSD (0.5% fee); ③ Accumulate TACm via staking rewards, then mint TiUSD via CDP.', 'ls.goDefi': 'Go to DeFi CDP Mint', 'ls.goSwap': 'Go to Stable Swap', 'ls.stakeTitle': 'Stake TACm → get stTACm', 'ls.address': 'Your address', 'ls.amount': 'Stake amount (TACm)', 'ls.stakeBtn': 'Stake',
      'ls.redeemTitle': 'Redeem stTACm → TACm', 'ls.address2': 'Your address', 'ls.amount2': 'Redeem amount (stTACm)', 'ls.redeemBtn': 'Redeem',
      'ls.myTitle': 'My Position', 'ls.query': 'Query', 'ls.myStaked': 'Staked (TACm)', 'ls.mySt': 'stTACm Balance', 'ls.myRedeem': 'Redeemable (TACm)',
      'ls.howTitle': 'How it works', 'ls.step': 'Step', 'ls.desc': 'Description',
      'ls.how1': 'Deposit TACm; the protocol mints stTACm at the current exchange rate (1:1 initially).',
      'ls.how2': 'Pooled assets are delegated to validators; rewards are auto-injected every block (v75) and the rate rises — your stTACm redeem value grows.',
      'ls.how3': 'Redeem TACm anytime at the current rate; stTACm also works across other DeFi protocols.',
      'wallet.onchain': ' My On-chain History', 'nav.exchange': 'Exchange', 'nav.dash': 'Dashboard',
      'nav.mining': 'Mining', 'nav.community': 'Community', 'nav.ai': 'Titan Ai', 'nav.tokens': 'Tokens', 'nav.nfts': 'NFT', 'nav.defi': 'DeFi', 'nav.tiusd': 'TiUSD Monitor', 'nav.ls': 'Staking', 'nav.governance': 'Governance',
      'nav.dapp': 'DApp Store', 'nav.agent': 'AI Agent', 'pill.dapp': 'DApp Store', 'pill.agent': 'AI Agent',
      'tx.logs': 'Event Logs', 'tx.logAddr': 'Contract Address', 'tx.decodedEvents': 'Decoded Events (ABI)', 'tx.gasUsed': 'Gas Used', 'tx.transfers': 'Token Transfers', 'tx.tContract': 'Contract', 'tx.tValue': 'Amount / Token ID', 'tx.tBlock': 'Block', 'tx.hash': 'Tx Hash', 'addr.transfers': 'Token Transfers', 'fc.title': 'Test Token Faucet', 'fc.sub': 'Developer ecosystem — free test TACm for your address', 'fc.claim': 'Claim Test Tokens', 'fc.limit': 'Daily limit per address', 'fc.addrLabel': 'Your tx0 address', 'fc.claimBtn': 'Claim', 'fc.how': 'How to use', 'fc.step1': 'Enter your wallet address and click claim', 'fc.step2': 'The tx is signed by the node key and lands on-chain in seconds', 'fc.step3': 'Check Wallet or Explorer to confirm (once per address per day)', 'fc.disabled': 'Disabled on mainnet (free assets no longer distributed)', 'fc.guide': 'TiUSD / USDT Circulation', 'fc.guide1': '① Claim TACm from the faucet → buy TiUSD on the exchange (TACM/TiUSD pair; stablecoin pegged 1:1 to USDT).', 'fc.guide2': '② DeFi lending (CDP): collateralize TACm to mint TiUSD.', 'fc.guide3': '③ TACm/TiUSD → USDT: trade on TACM/USDT and TiUSD/USDT markets (limit/market/swap).', 'fc.guide4': '④ Cross-chain: TACM/TiUSD/USDT can be bridged to BSC/Ethereum and other major chains.',
      'pill.ai': 'Titan Ai',
      'ai_assistant': 'Titan Ai', 'ai_sub': 'Titan Ai — on-chain assistant, instant answers',
      'ai.compliance': 'Write actions need your confirmation · node never signs for you · investment has risk',
      'ai.modeAssist': 'Assistant', 'ai.modeAgent': 'Agent',
      'ai.marketCard': 'AI Model Market', 'ai.marketCardSub': 'Register / subscribe / call on-chain models',
      'ai.verifyCard': 'Cross-chain AI Verify', 'ai.verifyCardSub': 'Inference result verification network',
      'ai.wallet': 'Wallet: ', 'ai.engine': 'AI engine: ', 'ai.probing': 'Probing…',
      'ai_suggest_status': 'Node status', 'ai_suggest_block': 'Latest block', 'ai_suggest_audit': 'On-chain audit',
      'ai_suggest_reward': 'Rewards', 'ai_suggest_miner': 'Online miners', 'ai_suggest_supply': 'Supply',
      'ai_suggest_join': 'Join network', 'ai_suggest_transfer': 'Transfer guide',
      'ai_placeholder': 'Ask anything about TACm… (e.g. node status)', 'ai_send': 'Ask',
      'pill.standby': 'Idle', 'pill.mining': 'Mining', 'pill.login': 'Sign In / Sign Up',
      'pill.tools': '☰ Tools', 'pill.wp': 'Whitepaper', 'pill.defi': 'DeFi', 'pill.assets': 'Assets Center', 'pill.c2c': 'C2C', 'pill.tokens': 'Tokens', 'pill.nfts': 'NFT', 'pill.en': 'EN', 'pill.lang': '中',
      'mining.start': '⚡ Start Mining', 'mining.stop': 'Stop Mining', 'mining.online': 'Mining Online', 'mining.offline': 'Offline',
      'mining.mech1': '· Per-block reward follows the supply model (≈1.5 TACm year 1, decaying 25% yearly, 52M total cap over 6 years): 45% split equally among online PoT nodes, 15% auto-contributes to the reward pool, remaining 40% split by online hashrate share.',
      'mining.mech2': '· Miners register by address (initial 1vCPU×1M + 2vGPU×2M) and keep heartbeats; rewards credit per block. Offline over ~1 hour (3700 blocks) stops sharing.',
      'mining.mech3': '· Hashrate grows via referrals: each member registered with your address grants +0.01 CPU / +0.02 GPU.',
      'pill.miner': 'Miner Status', 'nav.main': 'Main navigation', 'explorer.searchPh': 'Enter block height, tx hash or tx0 address…',
      'w3.title': 'MetaMask RPC', 'wallet.intro': 'Add the TACm Mainnet to MetaMask / Trust Wallet via custom RPC — full EIP-1559 (Type 2) transactions, event logs and receipts supported.',
      'wallet.rpc': 'RPC URL', 'wallet.chainId': 'Chain ID', 'wallet.symbol': 'Token Symbol', 'wallet.decimals': 'Decimals',
      'wallet.add': 'Add to MetaMask', 'wallet.verify': 'Verify Connection',
      'web3.connect': 'Connect Wallet', 'web3.addr': 'Address', 'web3.balance': 'Balance',
      'web3.noMeta': 'No MetaMask detected — install it first', 'web3.connectFail': 'Connection failed',
      'tacns.title': 'On-chain Naming (.tac)', 'tacns.intro': 'Register .tac names and bind addresses — bidirectional name↔address resolution.',
      'tacns.status': 'Registry', 'tacns.contract': 'Contract', 'tacns.deploy': 'Deploy Registry',
      'tacns.registerTitle': 'Register a name', 'tacns.register': 'Register', 'tacns.ownerPh': 'tx0… (owner address)',
      'tacns.years': 'Years', 'tacns.queryTitle': 'Lookup', 'tacns.query': 'Lookup', 'tacns.queryPh': 'label (e.g. titan) or tx0 address',
      'tacns.deployed': 'Deployed', 'tacns.undeployed': 'Not deployed', 'tacns.regOk': 'Registration submitted (pending block)',
      'tacns.regErr': 'Registration failed', 'tacns.fillReg': 'Fill in label, owner address and years', 'tacns.qEmpty': 'Enter a label or address',
      'tacns.noOwner': 'Unregistered', 'tacns.expiry': 'Expiry (unix)', 'tacns.owner': 'Owner', 'tacns.resolved': 'Resolved address',
      'tacns.needDeploy': 'Deploy the registry first', 'tacns.deployOk': 'Deploy tx submitted', 'tacns.deployErr': 'Deploy failed',
      'tacns.calldataNote': 'setAddr / transfer / renew: sign with your own key and submit via POST /tx/submit',
      'wallet.multiTitle': 'Multi-Sig Wallet', 'wallet.intro': 'N/M threshold multi-sig wallet — multi-sig fund management with EIP-1271 contract signature verification.',
      'wallet.status': 'Wallet', 'wallet.contract': 'Contract', 'wallet.owners': 'Owners', 'wallet.threshold': 'Threshold',
      'wallet.deploy': 'Deploy Wallet', 'wallet.deployed': 'Deployed', 'wallet.undeployed': 'Not deployed',
      'wallet.deployOk': 'Deploy tx submitted', 'wallet.deployErr': 'Deploy failed',
      'wallet.ownerMgmt': 'Owner Management', 'wallet.ownerPh': 'owner address (tx0 or 0x)',
      'wallet.hdTitle': 'Key Management (HD)', 'wallet.hdHint': 'Standard BIP-39/32/44 — one mnemonic manages all accounts, importable into MetaMask etc.',
      'wallet.hdNew': 'Create HD Wallet (standard mnemonic)', 'wallet.hdPath': 'Path', 'wallet.hdRestore': 'Import HD wallet (mnemonic)',
      'wallet.hdRestorePh': 'paste 12/15/18/21/24-word mnemonic', 'wallet.hdDerive': 'Derive',
      'wallet.hdSetActive': 'Set as My Wallet', 'wallet.hdActive': 'Current Wallet',
      'wallet.advanced': 'Advanced (Multi-Sig Contract Wallet)',
      'wallet.addOwner': 'Add Owner', 'wallet.removeOwner': 'Remove Owner',
      'wallet.thresholdSet': 'Set Threshold', 'wallet.setThreshold': 'Set', 'wallet.fillThreshold': 'Enter a threshold ≥ 1',
      'wallet.submitTitle': 'Submit Transaction (node-signed)', 'wallet.toPh': 'Target address', 'wallet.valuePh': 'Amount (TACm)',
      'wallet.submit': 'Submit Transaction', 'wallet.fillSubmit': 'Enter target address and amount',
      'wallet.approveTitle': 'Approve Transaction (v/r/s signature)', 'wallet.noncePh': 'nonce', 'wallet.approve': 'Approve',
      'wallet.fillApprove': 'Fill in to/value/v/r/s',
      'wallet.queryTitle': 'Lookup', 'wallet.isownerPh': 'Check if an address is owner', 'wallet.query': 'Lookup',
      'wallet.ownersList': 'Owners', 'wallet.empty': 'No owners yet',
      'wallet.fillOwner': 'Enter an owner address', 'wallet.opOk': 'Submitted (pending block)', 'wallet.opErr': 'Operation failed',
      'wallet.isOwnerYes': 'Is owner', 'wallet.isOwnerNo': 'Not an owner',
      'wallet.calldataNote': 'For self-signing flows (e.g. submit), get calldata via /tacwallet/calldata, sign with your private key, then POST /tx/submit.',
      'gov.title': 'On-chain Governance', 'gov.intro': 'Decentralized DAO — anyone can propose, one-address-one-vote, majority executes fund transfers.',
      'gov.status': 'Contract', 'gov.contract': 'Contract Address', 'gov.nextPid': 'Next Proposal ID',
      'gov.deploy': 'Deploy Governor', 'gov.proposeTitle': 'Create Proposal', 'gov.targetPh': 'Target address (0x…)', 'gov.valuePh': 'Amount (TACm)',
      'gov.propose': 'Propose', 'gov.voteTitle': 'Vote (node-signed, one address one vote)', 'gov.pidPh': 'Proposal ID',
      'gov.support': 'For', 'gov.against': 'Against', 'gov.vote': 'Vote',
      'gov.executeTitle': 'Execute Proposal (after majority)', 'gov.execute': 'Execute',
      'gov.queryTitle': 'Proposal Lookup', 'gov.query': 'Lookup',
      'gov.deployed': 'Deployed', 'gov.undeployed': 'Not deployed',
      'gov.deployOk': 'Deploy tx submitted', 'gov.deployErr': 'Deploy failed',
      'gov.fillPropose': 'Fill in target address and amount', 'gov.fillVote': 'Fill in proposal ID and pick a side', 'gov.fillPid': 'Fill in proposal ID',
      'gov.opOk': 'Submitted (awaiting block)', 'gov.opErr': 'Operation failed',
      'gov.creator': 'Creator', 'gov.passed': 'Passed', 'gov.executed': 'Executed', 'gov.pending': 'Pending',
      'gov.propInfo': 'Proposal Info', 'gov.infoEmpty': 'No proposal yet or ID not found',
      'gov.daoTitle': 'On-chain Governance (DAO)', 'gov.paramTab': 'Parameter Governance', 'gov.bridgeTitle': 'Bridge Governance (Kill-switch)', 'gov.bridgeState': 'Status', 'gov.bridgePause': 'Pause Bridge', 'gov.bridgeResume': 'Resume Bridge', 'gov.bridgeNote': 'Admin-only after mainnet wallet login; pausing rejects all bridge operations.', 'gov.bridgeNeedLogin': 'Login to mainnet wallet (admin) required', 'gov.bridgeFail': 'Operation failed', 'gov.vaultTitle': 'DeFi Vault Parameter Governance', 'gov.vaultId': 'Vault ID', 'gov.vaultFee': 'Performance Fee (0-1)', 'gov.vaultMin': 'Min Deposit (TACm)', 'gov.vaultStatus': 'Status', 'gov.vaultApply': 'Apply', 'gov.vaultNote': 'Run GET /api/defi/vaults to list vault IDs.', 'gov.vaultNeedId': 'Enter a Vault ID', 'gov.vaultOk': 'Vault params updated', 'gov.vaultFail': 'Update failed', 'gov.daoTab': 'Contract DAO', 'svc.title': 'On-chain Services', 'svc.wallet': 'TAC Wallet (Assets + Multi-Sig)', 'svc.ns': 'Naming Service .tac', 'svc.gov': 'On-chain Governance (DAO)', 'vld.title': 'Validator Ranking', 'vld.colNode': 'Node', 'vld.colStake': 'Total Weight (TACm)', 'vld.colDeleg': 'Delegated Total (TACm)', 'vld.colNum': 'Delegators', 'vld.colStatus': 'Status', 'vld.loading': 'Loading…', 'vld.modalTitle': 'Validator Detail', 'vld.pubkey': 'Public Key', 'vld.firstSeen': 'First Seen', 'vld.lastActive': 'Last Active', 'vld.delegatorNum': 'Delegators', 'vld.totalStake': 'Total Stake', 'vld.colNode': 'Validator', 'vld.delegTotal': 'Delegated', 'vld.colStatus': 'Status', 'oracle.title': 'Price Oracle', 'oracle.intro': 'Real-time price feeds for major assets — a reliable price source for DeFi lending, derivatives and developers.', 'oracle.feeds': 'Price Feeds', 'oracle.query': 'Query & Override', 'oracle.symbol': 'Market', 'oracle.price': 'Price (USDT)', 'oracle.chg': '24h', 'oracle.source': 'Source', 'oracle.updated': 'Updated', 'oracle.lookupBtn': 'Lookup', 'oracle.feedHint': 'Manually override a feed (node operator)', 'oracle.feedBtn': 'Override', 'oracle.historyHint': 'Price history', 'oracle.historyBtn': 'View', 'aa.title': 'Account Abstraction', 'aa.intro': 'EIP-4337 smart accounts — authorize operations with UserOperations; the Bundler pays gas and executes contract calls on your behalf.', 'aa.submit': 'Submit UserOperation', 'aa.submitHint': 'Enter the smart account address and its ECDSA signature; the Bundler verifies authorization, pays gas and executes the target contract call.', 'aa.senderPh': 'Sender (smart account)', 'aa.pubkeyPh': 'Pubkey (compressed hex)', 'aa.sigPh': 'Signature (DER hex)', 'aa.targetPh': 'Target (contract address)', 'aa.callDataPh': 'Call Data (hex, e.g. 00)', 'aa.estimateBtn': 'Estimate Gas', 'aa.submitBtn': 'Submit Operation', 'aa.estGas': 'Estimated Gas', 'aa.estFee': 'Estimated Fee', 'aa.status': 'Operation Status', 'aa.statusBtn': 'Query', 'aa.colStatus': 'Status', 'aa.colTarget': 'Target Contract', 'aa.colTx': 'Tx Hash', 'aa.colGas': 'Gas Fee', 'aa.colTime': 'Submitted At', 'aa.how': 'How It Works', 'aa.how1': 'The holder signs the operation digest (sender/nonce/target/callData/fee) with the smart account key — the digest is Canonical + DoubleSHA256 computed by the node.', 'aa.how2': 'The node Bundler (offline EntryPoint) verifies the signature, pubkey↔sender match, valid hex callData and sane gas parameters.', 'aa.how3': 'Once verified, the node key pays gas and submits the contract call (memo=vm:call:); the operation is traceable in the Explorer.', 'aa.how4': 'v2 roadmap: on-chain EntryPoint contract + Paymaster (any account can pay gas).', 'aa.estimated': 'Estimated', 'aa.estimating': 'Estimating…', 'aa.submitting': 'Submitting…', 'aa.rpcDown': 'RPC unavailable', 'aa.failed': 'Operation failed', 'aa.txPrefix': 'Tx: ', 'aa.bundlerLabel': 'Bundler', 'aa.fillRequired': 'Fill sender, target, callData, pubkey and signature', 'aa.fillSender': 'Enter a sender address', 'aa.opsEmpty': 'No operations yet', 'aa.noncePh': 'Nonce', 'aa.callGasPh': 'Call Gas Limit', 'aa.verifGasPh': 'Verification Gas', 'aa.maxFeePh': 'Max Fee Per Gas', 'nav.aa': 'AA', 'pill.aa': 'Account Abstraction', 'api.title': 'Indexer API', 'api.intro': 'Public read-only query surface — blocks, transactions, addresses and chain stats for developers and tooling. All endpoints return JSON.', 'api.endpoints': 'Endpoints', 'api.base': 'Base URL:', 'api.statsDesc': 'Chain stats: height, block count, tx count, account count, mempool size.', 'api.blocksDesc': 'Block list (height descending). limit defaults to 20, max 100.', 'api.blockDesc': 'Single block (height or block hash) with its transactions.', 'api.txDesc': 'Single transaction (64-char hex hash).', 'api.txsDesc': 'Address transaction history. dir may be in / out.', 'api.addrDesc': 'Address account: balance, nonce, first/last active time and total tx count.', 'api.delegDesc': 'Staking delegation page: all on-chain delegations (amount desc, delegator/node/amount/time).', 'api.delegatorDesc': 'Delegator view: full delegation details + accumulated delegation rewards (coinbase:deleg on-chain).', 'api.validatorsDesc': 'Node ranking: weight (self+delegated) / delegation total / delegator count / status, weight desc.', 'api.usage': 'Usage', 'api.usage1': 'All endpoints are read-only; no gas or signature required.', 'api.usage2': 'Pagination cap: limit ≤ 100.', 'api.usage3': 'Errors: 404 (not found) / 400 (invalid param/address/hash) / 500 (internal).', 'api.usage4': 'Coexists with eth_* JSON-RPC: RPC for wallets/contracts, Indexer for explorers and analytics.', 'st.title': 'PoT Staking (Proof-of-Trust)', 'st.intro': 'Delegate TACm to a node — delegation increases the node\'s block weight (total_stake); records go on-chain and are consistent network-wide. Delegated assets are locked; undelegating returns them.', 'st.delegateTitle': 'Delegate / Undelegate', 'st.delegateHint': 'Enter your wallet address, target node address and amount. Delegating locks assets in the wallet ledger and records on-chain; undelegating returns them.', 'st.fromPh': 'From (your tx0 address)', 'st.validatorPh': 'Node address', 'st.amountPh': 'Amount (TACm)', 'st.delegateBtn': 'Delegate', 'st.undelegateBtn': 'Undelegate', 'st.myTitle': 'My Delegations', 'st.queryBtn': 'Query', 'st.colValidator': 'Node', 'st.colAmount': 'Delegated (TACm)', 'st.colTime': 'Time', 'st.validatorsTitle': 'Node Delegation Totals', 'st.colTotal': 'Total Delegated (TACm)', 'st.colCount': 'Delegators', 'st.rewardsTitle': 'Accumulated Delegation Rewards (on-chain coinbase:deleg)', 'st.unbondTitle': 'Unbonding', 'st.colComplete': 'Complete Height', 'st.colStatus': 'Status', 'st.colVStatus': 'Status',      'mining.mech4': '· The node maintains one default miner (not sharing rewards); if no online miners exist, that block\'s 40% miner share goes fully into the reward pool. No rewards while offline.',
      'mining.earnTitle': 'My Earnings (7d · on-chain coinbase)', 'mining.earnSub': 'Daily reward (TACm, green) / daily avg time warrant (blue, right axis)',
      'mining.earnDate': 'Date', 'mining.earnAmt': 'Reward TACm', 'mining.earnCnt': 'Payouts', 'mining.earnHash': 'Avg Time Warrant',
      'mining.twTitle': 'Time Warrant Settings (fixed)', 'mining.twDesc1': 'Base time warrant: earned while node/miner online (default 1.0)', 'mining.twDesc2': 'Bonus proof warrant: from on-chain proof (staking/referrals), default 2.0', 'mining.twDesc3': 'Referral code = your wallet address; share it for friends to fill in at signup.',
      'wallet.title': 'TAC Wallet', 'wallet.ledger': 'Recent Ledger (Audit)', 'wallet.transfer': 'Internal Transfer', 'wallet.confirm': 'Confirm Transfer',
      'dash.title': 'Dashboard', 'chain.mined': 'Total TACm Mined', 'chain.tiusd': 'TiUSD Circulation',
      'chain.pool': 'Reward Pool', 'chain.earned': 'My Earnings', 'chain.rate': 'Block Rate',
      'index.title': 'TAC Ai Smart Chain', 'index.overview': 'Chain Overview',
      'bn.wallet': 'Web3 Wallet', 'bn.walletSub': 'On-chain Assets & Transfers',
      'bn.mining': 'Mining', 'bn.miningSub': 'Hashrate & Rewards',
      'bn.exchange': 'Exchange', 'bn.exchangeSub': 'Swap & Orders',
      'ex.hot': 'Hot Markets', 'ex.tapTrade': 'Tap a coin to trade', 'ex.ref': 'Ref',
      'ex.onchain': 'On-chain', 'ex.live': 'Live', 'ex.refDisabled': 'Disabled: only on-chain assets (TACM/TiUSD/USDT) are tradable', 'ex.fee': 'Fee',
      'ex.verifyBtn': 'Verify Address', 'ex.verifyUn': 'Not verified', 'ex.verifyOK': 'Address verified: ', 'ex.verifyNeedLogin': 'Please log in as a member to verify your bound miner wallet address', 'ex.verifyFail': 'Verification failed, please try again later', 'ex.verifyFirst': 'Please tap "Verify Address" to bind the current miner wallet first', 'ex.verifyChanged': 'Account address changed, please re-verify',
      'ex.walletTitle': 'Exchange Wallet', 'ex.walletNone': 'Not connected — please link a wallet', 'ex.walletMiner': 'Link Miner Wallet', 'ex.walletNew': 'New Web3 Wallet', 'ex.walletChange': 'Change', 'ex.walletMinerOK': 'Miner wallet linked (current chain): ', 'ex.walletNeedLogin': 'Please log in as a member to link your miner wallet', 'ex.walletNewConfirm': 'A brand-new Web3 wallet will be created. Back up the private key immediately afterwards. Continue?', 'ex.walletNewOK': 'New Web3 wallet created: ', 'ex.walletWifLabel': 'Private key WIF (back up offline now; it cannot be recovered): ', 'ex.walletWifWarn': 'Write it down and keep it offline. Never share it; use it to restore the wallet on other devices.', 'ex.walletNewFail': 'Failed to create Web3 wallet, please try again later', 'ex.walletChangeConfirm': 'Change the exchange wallet? Current fields will be cleared.', 'ex.orderMarket': 'Market',
      'ex.refBook': 'Reference quote (valuation mirror) · no order book',
      'ex.refDepth': 'Reference quote · no live order book',
      'ex.best': 'Best',
      'ex.loading': 'Loading…',
      'ex.liveBook': 'Live quote · no order book',
      'ex.refSwap': 'Valuation swap · fee', 'ex.tradeOk': 'Swap done',
      'bn.community': 'Community', 'bn.communitySub': 'Feed & Rewards',
      'bn.live': 'Live', 'bn.liveSub': 'Community Live · Stream & Chat',
      'cm.brand': 'TAC Community', 'cm.home': 'Home', 'cm.video': 'Videos', 'cm.friends': 'Friends',
      'cm.market': 'Marketplace', 'cm.notify': 'Notifications', 'cm.me': 'Profile',
      'cm.back': 'Back', 'cm.switch': 'Switch', 'cm.profile': 'Profile', 'cm.myFriends': 'Your Friends',
      'cm.followersYou': 'Followers', 'cm.suggest': 'Suggested (People you may know)', 'cm.online': 'online',
      'cm.myWallet': 'My Wallet', 'cm.myMiner': 'My Miner', 'cm.exchange': 'Exchange', 'cm.myPosts': 'My Posts',
      'cm.chainAssets': 'On-chain Assets', 'cm.編輯': 'Edit', 'cm.刪除': 'Delete', 'cm.儲存': 'Save', 'cm.取消': 'Cancel', 'cm.載入中': 'Loading…',
      'cm.未命名': 'Unnamed', 'cm.對外顯示名稱': 'Display Name', 'cm.個人頭像': 'Avatar', 'cm.已儲存': 'Saved', 'cm.已更新': 'Updated',
      'cm.已刪除': 'Deleted', 'cm.編輯影片描述': 'Edit video description:', 'cm.確定刪除這則貼文': 'Delete this post?', 'cm.確定刪除這支影片': 'Delete this video?',
      'cm.編輯貼文內容': 'Edit post content:', 'cm.編輯個人資料': 'Edit Profile', 'cm.儲存失敗': 'Save failed', 'cm.編輯失敗': 'Edit failed', 'cm.刪除失敗': 'Delete failed',
      'cm.namePh': 'Enter display name...', 'cm.avaPh': 'https://... or /static/...',
      'cm.noVideo': 'No videos yet. Be the first to post one!',
      'cm.postsN': 'posts', 'cm.following': 'Following', 'cm.followers': 'followers', 'cm.videoTag': 'Video', 'cm.marketTag': 'Market', 'cm.openVideo': 'Open Video',
      'cm.sell': 'List Item', 'cm.listItem': 'Publish', 'cm.buyTitle': 'Confirm Purchase', 'cm.confirmPay': 'Confirm & Pay (one-click)',
      'cm.rateItem': 'Rate Item', 'cm.rating': 'Rating (1-5)', 'cm.submit': 'Submit Review',
      'cm.adCreate': 'Create Ad', 'cm.adPlace': 'Placement', 'cm.adPlaceRule': 'In-feed ads',
      'cm.adBudget': 'Total Budget', 'cm.adDaily': 'Daily Budget', 'cm.adTarget': 'Click-through URL', 'cm.adTargetRule': 'Opens on "Learn More"',
      'cm.adSubmit': 'Publish Ad (starts after payment)', 'cm.myAds': 'My Ads', 'cm.了解更多': 'Learn More',
      'cm.已售出': 'Sold', 'cm.購買': 'Buy', 'cm.評價': 'Review', 'cm.賣家': 'Seller', 'cm.鏈上費': 'Chain Fee',
      'cm.合計': 'Total', 'cm.處理中': 'Processing…', 'cm.商品 ID': 'Item ID',
      'cm.市集尚無商品': 'No items in marketplace yet', 'cm.載入失敗': 'Load failed', 'cm.請先輸入你的錢包地址': 'Enter your wallet address first',
      'cm.addrFromWallet': 'From wallet center', 'cm.walletNeedWalletCenter': 'Please select your wallet address (tx0) in the wallet center first', 'cm.walletNeedWalletCenterShort': 'Please select your wallet address in the wallet center first',
      'cm.請填寫商品名稱與價格': 'Fill in item name and price', 'cm.上架失敗': 'Publish failed: ',
      'cm.廣告已建立': 'Ad created (ID #', 'cm.請填寫廣告標題與總預算': 'Fill in ad title and total budget', 'cm.發佈失敗': 'Publish failed: ',
      'cm.投放中': 'Active', 'cm.待付款': 'Awaiting Payment', 'cm.付款投放': 'Pay & Activate',
      'cm.動態流': 'Feed', 'cm.影片流': 'Video Feed', 'cm.動態流+影片流': 'Feed + Video Feed',
      'cm.總預算': 'Total Budget', 'cm.每日': 'Daily', 'cm.曝光': 'Impressions', 'cm.點擊': 'Clicks', 'cm.尚無廣告': 'No ads yet',
      'cm.確認支付廣告預算並開始投放？': 'Confirm paying ad budget and starting delivery?', 'cm.廣告已投放': 'Ad activated (ID #',
      'cm.付款失敗': 'Payment failed: ', 'cm.請填寫廣告標題與總預算': 'Fill in ad title and total budget',
      'cm.login': 'Login', 'cm.live': 'Live', 'cm.newPost': 'Create post', 'cm.upload': 'Upload photo/video',
      'cm.kPost': 'Post', 'cm.kVideo': 'Video', 'cm.kLive': 'Live', 'cm.kMarket': 'Marketplace', 'cm.post': 'Publish',
      'bn.dex': 'DEX', 'bn.dexSub': 'Liquidity & Swap',
      'bn.bridge': 'Bridge', 'bn.bridgeSub': 'Cross-chain asset interoperability (TACm ↔ BSC)',
      'br.status': 'Relay Status', 'br.assets': 'Asset Mappings', 'br.lock': 'Lock to',
      'br.asset': 'Asset', 'br.srcAddr': 'Source address (tx0)', 'br.dstAddr': 'Target address (0x BSC)', 'br.amount': 'Amount',
      'br.lockBtn': 'Lock & Send', 'br.history': 'Bridge History', 'br.historyHint': 'by address',
      'br.query': 'Query', 'br.historyPh': 'Enter an address to view bridge transactions',
      'bn.nfts': 'NFT Gallery', 'bn.nftsSub': 'Digital Collectibles',
      'bn.ai': 'Titan Ai', 'bn.aiSub': 'On-chain assistant · single AI entry',
      'bn.explorer': 'Explorer', 'bn.explorerSub': 'On-chain Search',
      'index.heroTag': 'AI-native blockchain · EVM compatible · EIP-1559 · L2 Rollup · BSC Bridge',
      'feat.wallet': 'Assets & Transfer', 'feat.mining': 'Hashrate & Rewards', 'feat.explorer': 'On-chain Search',
      'feat.exchange': 'Exchange', 'feat.community': 'Community', 'feat.dex': 'DEX', 'feat.nfts': 'NFT', 'feat.ai': 'Titan Ai',
      'index.v1t': 'Your Assets Are Yours', 'index.v1s': 'Your bank account is an entry in someone else\'s database. TAC is an alternative network where you hold your assets directly.',
      'index.v2t': 'Privacy by Default', 'index.v2s': 'No account tied to your name, no company watching your balance. Your on-chain activity is yours.',
      'index.v3t': 'Never Offline', 'index.v3s': 'Multi-node decentralized network with 100% uptime. No single server can take your chain down.',
      'index.v4t': 'AI-Native', 'index.v4s': 'On-chain AI assistant + community rewards. TAC is built for the intelligent era.',
      'index.ctaWallet': 'Open Wallet', 'index.ctaMining': 'Start Mining', 'index.ctaJoin': 'Join Network', 'index.ctaExplorer': 'Explorer',
      'index.latestTx': 'Latest Transactions',       'ld.tag': 'An autonomous network built for the intelligent era: PoT + PoS + BFT consensus, EVM compatibility, EIP-1559, L2 Rollup, BSC Bridge, on-chain AI - your assets, your community, your future, all on-chain.',
      'ld.enter': 'Enter System', 'ld.wallet': 'Open Wallet', 'ld.mining': 'Start Mining', 'ld.explorer': 'Block Explorer',
      'ld.sec1t': 'Why TAC Ai Smart Chain?', 'ld.sec1s': 'Not another centralized server - a network that truly belongs to its users. Here are six key advantages.',
      'ld.d1t': 'Truly Decentralized', 'ld.d1s': 'Three-layer consensus (PoT + PoS + BFT) with dynamic time difficulty; any node can join block production and verification. No single company or server can shut down or tamper with this chain.',
      'ld.d2t': 'Cryptographic Security', 'ld.d2s': 'ECDSA signatures, SHA-256 chained hashes, SPV finality and Merkle verification; built-in full-ledger replay audit verifies coinbase/splits every 100 blocks and flags inconsistencies instantly.',
      'ld.d3t': 'Your Assets Are Yours', 'ld.d3s': 'No name-bound accounts, no company watching your balance. You hold the private keys; built-in exchange and bridge let assets move freely.',
      'ld.d4t': 'Complete Ecosystem', 'ld.d4s': 'Built-in wallet, exchange, DEX, DeFi, IDO, NFT, Token Studio, community marketplace, on-chain governance - one account closes the loop from token creation to trading to governance.',
      'ld.d5t': 'Interoperable', 'ld.d5s': 'EVM compatible with eth_* JSON-RPC: MetaMask connects directly, Type-2 (EIP-1559) transactions supported; a real BSC bridge moves assets across mainnets smoothly.',
      'ld.d6t': 'AI-Native', 'ld.d6s': 'On-chain AI assistant queries balances, transactions, blocks and governance params; community rewards are recorded on-chain. TAC is built for the intelligent era.',
      'ld.specT': 'Mainnet Specifications', 'ld.enterT': 'Get Started','index.height': 'Chain Height', 'index.final': 'Finalized Height', 'index.diff': 'Difficulty', 'index.mempool': 'Pending Tx',
      'auth.register': 'Register', 'auth.login': 'Log In', 'auth.email': 'Email', 'auth.password': 'Password',
      'tokens.title': 'Token Studio', 'tokens.sub': 'On-chain standard tokens (ERC-20) · Official issuance · Live blocks',
      'tokens.count': 'tokens', 'tokens.deploy': 'Issue Token', 'tokens.name': 'Name (≤32 chars)', 'tokens.symbol': 'Symbol (≤32 chars)',
      'tokens.supply': 'Total Supply', 'tokens.from': 'Issuer address (optional; 100 TACm deploy fee deducted here, node pays if empty)', 'tokens.usage': 'Usage: create AMM liquidity pools on DEX, ERC-20 transfer/approve/bridge, IDO subscription, community market payments.', 'tokens.deploy_btn': '⚡ Issue', 'tokens.detail': 'Token Details', 'tokens.list': 'Issued Tokens',
      'tokens.select': '← Select a token from list', 'tokens.mybal': 'My Balance', 'tokens.contract': 'Contract Address',
      'tokens.to': 'Transfer To (tx0 addr)', 'tokens.amount': 'Amount', 'tokens.transfer': '⇄ Transfer',
      'nfts.title': 'NFT Studio', 'nfts.sub': 'On-chain standard NFTs (ERC-721) · Official issuance · Live blocks',
      'nfts.count': 'NFT collections', 'nfts.deploy': 'Deploy NFT Collection', 'nfts.deploy_btn': '⚡ Deploy',
      'nfts.detail': 'NFT Details', 'nfts.list': 'NFT Collections', 'nfts.mybal': 'My Holdings',
      'nfts.contract': 'Contract Address', 'nfts.mint_to': 'Mint To (tx0 addr)', 'nfts.token_id': 'Token ID',
      'nfts.mint': '⚡ Mint', 'nfts.owner_query': 'Query Owner', 'nfts.owner_of': 'Token Owner',
      'nfts.token_uri': 'Token URI', 'nfts.transfer_from': 'Transfer From (tx0)', 'nfts.transfer_to': 'Transfer To (tx0)',
      'nfts.transfer_btn': '⇄ Transfer', 'nfts.supply': 'Minted Count', 'nfts.uri_placeholder': 'e.g. 1 (numeric)',
      'nfts.hint_owner': 'Only the NFT owner (or an approved caller) can transfer (node-signed calls act as the node account)',
      'nfts.approve_zone': 'Approvals (ERC-721)', 'nfts.approve_to': 'Approve To (tx0 addr)',
      'nfts.approve_btn': '⚡ Approve', 'nfts.get_approved': 'Query getApproved', 'nfts.approved_of': 'Approved caller (this token)',
      'nfts.operator': 'Operator (tx0)', 'nfts.owner_addr': 'Owner (tx0)', 'nfts.set_all_true': '⇄ Approve All (true)', 'nfts.set_all_false': '⇄ Revoke (false)',
      'nfts.operator_approved': 'Query isApprovedForAll', 'nfts.approve_hint': 'approve grants a single token to one address; setApprovalForAll grants an operator full management of your tokens (node-signed calls act as the node account)',
      'nav.dex': 'DEX', 'nav.orderbook': 'OrderBook', 'nav.aiv': 'AI Verify', 'pill.dex': 'DEX', 'pill.orderbook': 'OrderBook', 'pill.aiv': 'AI Verify',
      'nav.dexv3': 'DEX V3', 'nav.perp': 'Perpetuals', 'nav.aimarket': 'AI Model Market', 'pill.dexv3': 'DEX V3', 'pill.perp': 'Perpetuals', 'pill.aimarket': 'AI Market',
      'ex.uidHint': 'Account address = login-bound address (same mainnet chain); guest addresses may differ per browser session, please log in before trading.',
      'ex.back': 'Back to Exchange', 'ex.ecoTitle': 'Advanced Trading & Ecosystem', 'ex.ecoHint': 'Unified wallet · shared with spot',
      'ex.dex': 'DEX', 'ex.dexS': 'AMM pools · instant swap', 'ex.dexv3': 'DEX V3', 'ex.dexv3S': 'Concentrated liquidity',
      'ex.perp': 'Perpetuals', 'ex.perpS': 'Leveraged long/short · funding', 'ex.aimarket': 'AI Model Market', 'ex.aimarketS': 'Deploy & call on-chain models',
      'ex.aiverify': 'AI Verify', 'ex.aiverifyS': 'Cross-chain inference verification',
      'dex.title': 'On-Chain DEX (AMM)', 'dex.sub': 'Constant product · On-chain settlement · 0.3% fee',
      'dex.pools_word': 'pools', 'dex.fee': 'Fee 0.30%',
      'dex.pools': 'Liquidity Pools', 'dex.pools_hint': 'List tokens to create pools', 'dex.empty': 'No pools yet',
      'dex.create': 'Create Pool', 'dex.token0': 'Token 0', 'dex.token1': 'Token 1', 'dex.create_btn': 'Create Pool',
      'dex.liquidity': 'Liquidity', 'dex.pool': 'Pool', 'dex.amount0': 'Amount (Token 0)', 'dex.amount1': 'Amount (Token 1)',
      'dex.add_btn': '+ Add Liquidity', 'dex.mypos': 'My LP Shares', 'dex.remove_btn': '- Remove All Liquidity',
      'dex.swap': 'Swap', 'dex.swap_hint': 'Constant product auto-slippage',
      'dex.token_in': 'Input Token', 'dex.amount_in': 'Input Amount', 'dex.swap_btn': '⇄ Swap',
      'dex.slippage': 'Slippage tolerance', 'dex.slippage_custom': 'Custom slippage (%)',
      'dex.il_title': 'Impermanent Loss Risk', 'dex.il_body': 'After providing liquidity, if token prices swing, your exit value may be lower than simply holding both tokens (IL = price-volatility loss, partially offset by fees). Higher-volatility pairs carry more risk.', 'dex.il_ack': 'I understand and accept the impermanent loss risk',
      'dex.audit': 'Security Audit', 'dex.audit_hint': 'On-chain verifiable',
      'dex.audit1': 'Core chain consistency (PoS/PoT coinbase)', 'dex.audit2': 'EVM atomicity & gas governance', 'dex.audit3': 'Bridge multisig + SPV', 'dex.audit4': 'Full ledger replay (ledger audit)', 'dex.audit5': 'Third-party institutional audit', 'dex.audit_note': 'Audits are on-chain verifiable: explorer tx details, ledger audit API and bridge event logs are all public.',
      'dex.farm': 'Liquidity Mining (Farm)', 'dex.farm_hint': 'Stake LP shares to earn reward tokens', 'dex.farm_empty': 'No farms yet: create a pool and fund rewards first',
      'dex.farm_stake': 'Stake', 'dex.farm_unstake': 'Unstake', 'dex.farm_claim': 'Claim Rewards',
      'gov.title': 'On-Chain Governance', 'gov.sub': 'On-chain proposals · weighted voting · auto-execution',
      'gov.proposals': 'All Proposals', 'gov.propose': 'Create Proposal', 'gov.params': 'Governable Parameters', 'gov.rules': 'Governance Rules',
      'gov.window': 'Voting Window', 'gov.threshold': 'Pass Threshold', 'gov.quorum': 'Quorum', 'gov.timelock': 'Timelock', 'gov.create': 'Submit Proposal',
      'gov.title_f': 'Title', 'gov.desc': 'Description', 'gov.type': 'Type', 'gov.param': 'Parameter',
      'gov.value': 'Value', 'gov.address': 'Your Address', 'gov.amount': 'Amount (TACm)', 'gov.none': 'No proposals yet', 'gov.myRole': 'My Role', 'gov.adminYes': 'Admin ✓', 'gov.adminNo': 'Regular Member', 'gov.adminGuest': 'Not logged in (admin only for bridge)', 'gov.adminNote': 'Admins can promote/demote other accounts (admin-only).', 'gov.adminBtn': 'Apply', 'gov.adminOk': 'Admin role updated', 'gov.adminFail': 'Update failed, try again', 'gov.adminNeedEmail': 'Enter email',
      'gov.tp_param': 'Parameter Change', 'gov.tp_treasury': 'Treasury Spend', 'gov.tp_meta': 'Meta Decision',
      'gov.pk_bt': 'Block Time (block_time)', 'gov.pk_d': 'Base Difficulty (difficulty)',
      'gov.pk_f': 'Fee Basis (tx_fee_bps)', 'gov.pk_t': 'TiUSD Floor (tiusd_floor_bps)', 'gov.pk_q': 'Quorum (gov_quorum_pct)', 'gov.pk_tl': 'Timelock (gov_timelock_blocks)', 'gov.pk_pm': 'Proposer Min (gov_proposer_min)', 'gov.pk_ts': 'Total Supply (gov_total_supply)',
      'common.copy': 'Copy', 'common.refresh': 'Refresh', 'common.close': 'Close', 'common.confirm': 'Confirm', 'common.loading': 'Loading…',
      'net.audit': ' Chain Audit (Blocks · Txs · Contracts)',
      'net.alertsTitle': 'Supply Alerts (TiUSD)',
      'net.auditTip': 'Verifying last 100 blocks + full-ledger replay…',
      'net.auditNA': 'Audit unavailable', 'net.auditNA2': 'Cannot verify',
      'net.auditInc': 'Incremental audit (from block', 'net.auditFull': 'Full audit',
      'net.auditBlocks': 'Audited Blocks', 'net.auditCoin': 'Coinbase == Reward', 'net.auditPool': 'Pool 15%',
      'net.auditSplit': 'Node 45% · Miner 40%', 'net.auditMined': 'Total Mined', 'net.auditPoolBal': 'Reward Pool',
      'net.auditPoolShare': 'Pool On-Chain', 'net.auditMiners': 'Online Miners',
      'net.txChecked': 'Txs Replayed', 'net.txSig': 'Sig / Nonce / Balance', 'net.txContract': 'Contracts', 'net.ledger': 'Ledger',
      'net.bad': ' bad',
      'net.good': ' ok',
      'net.ledgerOK': 'Ledger consistent', 'net.ledgerOKDesc': 'Every tx passed signature/nonce/balance, contracts valid, total balance == coinbase minted',
      'net.ledgerBAD': 'Ledger anomaly', 'net.ledgerBADDesc': 'Tx/contract layer anomaly found, check immediately',
      'net.onchainOK': 'On-chain consistent', 'net.onchainOKDesc': 'Every block coinbase equals block reward (node 45% / pool 15% / miner 40%), no over/under mint, supply and rewards represent on-chain truth',
      'net.onchainBAD': 'Anomaly found', 'net.onchainBADDesc': 'On-chain rewards inconsistent, check immediately',
      'net.issueList': 'Issues: ',
      'node.badge': 'NODE', 'node.title': 'Node Branded Console', 'node.h1': 'Standalone Node Console',
      'node.p1': 'The node front-end is now an independent brand (TAC Ai Smart Chain — Node), living in its own folder (cmd/nodeweb + internal/nodeui), fully separated from mainnet user features.',
      'node.encNote': '🔒 P2P traffic is encrypted by default since v65 (ECDH+AES-GCM), auto-downgrading to plaintext with legacy peers; add -p2p-plain to force plaintext. The node console Peers table shows 🔒 encrypted / ⚠ plaintext per channel.',
      'node.acc1': 'Node Dashboard', 'node.acc2': 'Sync Status', 'node.acc3': 'P2P Peers', 'node.acc4': 'Nodes', 'node.acc5': 'Latest Blocks',
      'node.cmdTitle': 'Start Command', 'node.cmdHint': 'Run one command at a time on your node server:',
      'node.seedTitle': 'Seed Node', 'node.step1': 'Download the package and unpack it.', 'node.step2': 'Start with the same genesis node (follow mode).', 'node.step3': 'Open your branded node console.', 'node.step4': 'The node syncs automatically, validates and splits the 45% node reward among online nodes.',
      'node.dlTitle': 'Download Node Binary (pre-built)', 'node.dlDesc': 'Official pre-built node binary — ready to run (no Go, no compiling):', 'node.dlNote': 'Setup: download, install and run under systemd. The node key is auto-generated = your payout address; back up node_key.json.',
      'ld2.navEco': 'Ecosystem', 'ld2.navWhy': 'Why TAC', 'ld2.navHow': 'How It Works', 'ld2.navSpec': 'Specs', 'ld2.navJoin': 'Join Network',
      'ld2.enter': 'Launch App', 'ld2.tag2': 'The all-in-one blockchain for the smart age — consensus, EVM, L2, bridges and AI on a single network. Your assets, your community, your future — on-chain.',
      'ld2.cta1': 'Get Started', 'ld2.cta2': 'Start Mining', 'ld2.trust2': '✓ Audited On-Chain', 'ld2.trust3': '✓ Full Ecosystem',
      'ld2.online': 'Miners Online', 'ld2.ecoT': 'One Complete On-Chain Ecosystem', 'ld2.ecoS': 'Wallet · Exchange · Mining · DeFi · NFT · Governance',
      'ld2.svcT': 'Explore the Ecosystem', 'ld2.svcS': 'Everything runs on the same chain — one account, one balance sheet, full loop.',
      'ld2.go': 'Open →', 'ld2.s1t': 'Wallet', 'ld2.s1s': 'Multi-asset account with full on-chain history',
      'ld2.s2t': 'Mining', 'ld2.s2s': 'Hashrate share rewards, real-time payout',
      'ld2.s3t': 'Exchange', 'ld2.s3s': 'Orderbook, flash swap & on-chain trading',
      'ld2.s4t': 'DEX', 'ld2.s4s': 'AMM liquidity pools, instant swaps',
      'ld2.s5t': 'DeFi', 'ld2.s5s': 'Yield farming, lending & Vault',
      'ld2.s6t': 'NFT', 'ld2.s6s': 'ERC-721 collectibles & trading',
      'ld2.howT': 'Get Started in 3 Steps', 'ld2.howS': 'From wallet to governance — everything happens on-chain.',
      'ld2.h1t': 'Create Your Wallet', 'ld2.h1s': 'Generate your address & key in one click — your assets, your keys.',
      'ld2.h2t': 'Pick Your Service', 'ld2.h2s': 'Mine, trade, farm or collect — enter any ecosystem door.',
      'ld2.h3t': 'Shape the Network', 'ld2.h3s': 'Propose, vote and auto-execute on-chain parameters.',
      'ld2.ctaT': 'Ready to Go On-Chain?', 'ld2.ctaS': 'Join the mainnet — mine, trade or deploy your contracts.',
      'ld2.f1': 'Quick Links', 'ld2.f2': 'Network', 'ld2.cr': '© 2026 TAC Ai Smart Chain. All rights reserved.',

    
      'pill.sign': 'Sign Tool',
      'pill.contracts': 'Contract Verifier',
      'pill.zk': 'ZK/VRF',
      'contract.title': 'Contract Verifier',
      'contract.sub': 'Explore deployed contracts and read / verify them',
      'contract.listTitle': 'Deployed Contracts',
      'contract.thAddr': 'Address',
      'contract.thName': 'Name',
      'contract.thSymbol': 'Symbol',
      'contract.thSupply': 'Supply',
      'contract.loading': 'Loading…',
      'contract.readTitle': 'Read Contract',
      'contract.readSub': 'ABI-encoded eth_call · no state change',
      'contract.reload': 'Reload',
      'contract.loadAbi': 'Load ABI',
      'contract.read': 'Read',
      'contract.copy': 'Copy',
      'contract.verifyTitle': 'Verify Contract',
      'contract.verifySub': 'attach ABI · enables one-click read',
      'contract.verify': 'Verify',
      'contract.infoTitle': 'Contract Details',
      'contract.infoSub': 'code · storage · balance',
      'sign.title': 'Signature Tool',
      'sign.sub': 'Developer signing utilities — message signatures and address recovery',
      'sign.account': 'Node Account',
      'sign.addr': 'Node address',
      'sign.ps': 'Personal Sign (EIP-191)',
      'sign.psSub': 'node key · 0x65-byte signature',
      'sign.utf8': 'UTF-8 text',
      'sign.hex': 'Hex (0x…)',
      'sign.sign': 'Sign',
      'sign.copy': 'Copy',
      'sign.verify': 'Recover Signer (personal_ecRecover)',
      'sign.recover': 'Recover',
      'sign.td': 'Typed Data v4 (EIP-712)',
      'sign.tdSub': 'official Mail example pre-filled',
      'sign.tdSign': 'Sign typed data',
      'sign.legacy': 'eth_sign (legacy) + eth_ecRecover',
      'sign.legacySub': 'keccak256(message) — compatibility',
      'cm.editProfile': 'Edit',
      'cm.dispName': 'Display Name',
      'cm.avatar': 'Avatar (image URL or uploaded URL)',
      'cm.cancel': 'Cancel',
      'cm.save': 'Save',
      'tx.status': 'Status',
      'tx.confirmations': 'Confirmations',
      'tx.blocks': 'Blocks',
      'tx.block': 'Block',
      'tx.fee': 'Fee',
      'tx.gasPrice': 'Gas Price (effective)',
      'cm.loading': 'Loading…',
      'hz.dexv3': 'DEX V3', 'hz.dexv3s': 'Concentrated liquidity swap',
      'hz.perp': 'Perpetuals', 'hz.perps': 'Leveraged long/short trading',
      'hz.aimarket': 'AI Model Market', 'hz.aimarkets': 'Deploy & call models',
      'hz.orderbook': 'OrderBook', 'hz.orderbooks': 'Limit order depth trading',
      'hz.aiverify': 'AI Verify', 'hz.aiverifys': 'Content authenticity proof',
      'hz.oracle': 'Oracle', 'hz.oracles': 'On-chain price feeds',
      'hz.zk': 'ZK/VRF', 'hz.zks': 'Zero-knowledge & verifiable randomness',
      'hz.faucet': 'Faucet', 'hz.faucets': 'Claim test tokens',
      'hz.ens': 'ENS Names', 'hz.enss': 'On-chain domain resolution',
      'hz.contracts': 'Contract Verifier', 'hz.contractss': 'Source code verification',
      'hz.sdk': 'SDK Tools', 'hz.sdks': 'Developer toolkit',
      'hz.sign': 'Sign Tool', 'hz.signs': 'Message signing',
}
  };
  // 全域雙向字典（長短語優先替換，覆蓋所有頁面靜態文字）。
  var zh2en = {
    '區塊瀏覽器': 'Block Explorer', '首頁': 'Home', '錢包': 'Wallet', '交易所': 'Exchange',
    '儀表板': 'Dashboard', '挖礦': 'Mining', '社群': 'Community', '治理': 'Governance', '鏈上治理': 'On-Chain Governance',
    '提案': 'Proposal', '投票': 'Vote', '投票窗口': 'Voting Window', '通過門檻': 'Pass Threshold', '可治理參數': 'Governable Parameters',
    '發起提案': 'Create Proposal', '提交提案': 'Submit Proposal', '參數調整': 'Parameter Change', '國庫支出': 'Treasury Spend',
    '治理決議': 'Meta Decision', '全鏈總產出 TACm': 'Total TACm Mined', '白皮書': 'Whitepaper',
    '代幣工作室': 'Token Studio', '發行代幣': 'Issue Token', '代幣詳情': 'Token Details', '已發行代幣': 'Issued Tokens',
    '代幣': 'Tokens', '名稱（≤32 字元）': 'Name (≤32 chars)', '代號（≤32 字元）': 'Symbol (≤32 chars)',
    '總供應量': 'Total Supply', '我的餘額': 'My Balance', '合約地址': 'Contract Address',
    '轉帳至（tx0 地址）': 'Transfer To (tx0 addr)', '代簽轉帳': 'Transfer', '← 從列表選擇代幣': '← Select a token from list',
    '個代幣': ' tokens', '尚無代幣': 'No tokens issued yet', '名稱': 'Name', '代號': 'Symbol', '數量': 'Amount',
    '待機': 'Idle', '挖礦中': 'Mining', '登入/註冊': 'Sign In / Sign Up', '登入': 'Sign In', '註冊': 'Sign Up',
    '鏈上 DEX（AMM）': 'On-Chain DEX (AMM)', '恆定乘積做市 · 鏈上結算 · 0.3% 手續費': 'Constant product · On-chain settlement · 0.3% fee',
    '個池': ' pools', '手續費 0.30%': 'Fee 0.30%', '流動性池': 'Liquidity Pools',
    '發行代幣後可建池交易': 'List tokens to create pools', '尚無流動性池': 'No pools yet',
    '建立流動性池': 'Create Pool', '代幣 0': 'Token 0', '代幣 1': 'Token 1', '建立池': 'Create Pool',
    '流動性': 'Liquidity', '池': 'Pool', '數量（代幣 0）': 'Amount (Token 0)', '數量（代幣 1）': 'Amount (Token 1)',
    '＋ 注入流動性': '+ Add Liquidity', '我的 LP 份額': 'My LP Shares', '－ 退出全部流動性': '- Remove All Liquidity',
    '流動性挖礦（Farm）': 'Liquidity Mining (Farm)', '質押 LP 份額賺取獎勵代幣': 'Stake LP shares to earn reward tokens',
    '尚無農場：先建立流動性池並注資獎勵': 'No farms yet: create a pool and fund rewards first',
    '質押': 'Stake', '解除質押': 'Unstake', '領取獎勵': 'Claim Rewards',
    '即時兌換': 'Swap', '恆定乘積自動滑價': 'Constant product auto-slippage',
    '輸入代幣': 'Input Token', '輸入數量': 'Input Amount', '⇄ 兌換': '⇄ Swap',
    '儲備: ': 'Reserve: ', 'LP: ': 'LP: ', '選擇池': 'Select pool', '選擇代幣': 'Select token',
    '選用': 'Select', '載入失敗: ': 'Load failed: ', '交易上鏈中…': 'Broadcasting on-chain…',
    '請選擇兩個代幣': 'Pick two tokens', '請選擇池': 'Pick a pool', '請填寫池與數量': 'Fill pool and amounts',
    '請填寫兌換資訊': 'Fill swap info', '無份額可退出': 'No shares to remove', '已退出 ✓': 'Removed ✓',
    '已注入，份額 ': 'Added, shares ', '獲得 ': 'You receive ', '含 0.3% 手續費': 'incl. 0.3% fee',
    '池 ': 'Pool ', ' 已建立 ✓': ' created ✓', '失敗': 'Failed', '請選擇': 'Select', '請填寫': 'Fill in',
    '選用': 'Select',
    '工具': 'Tools', '登出': 'Log Out', '未登入': 'Not Signed In', '我的錢包': 'My Wallet', '我的礦機': 'My Miner',
    '登入註冊': 'Sign In / Sign Up', '官方網站': 'Official Site', '官網': 'Official Site',
    '社群陸續開放': 'Community Opening Soon', '更多': 'More',
    '錢包接入': 'Wallet Integration', 'RPC URL': 'RPC URL', '代幣符號': 'Token Symbol', '小數位': 'Decimals',
    '加到 MetaMask': 'Add to MetaMask', '驗證連線': 'Verify Connection', '請先安裝 MetaMask 後重試': 'Install MetaMask first and retry', '檢查中…': 'Checking…',
    '鏈頂高度': 'Chain Height', '最終化高度': 'Finalized Height', '當前難度': 'Difficulty', '待打包交易': 'Pending Txs',
    '全鏈總產出': 'Total Mined', '全鏈總產出 TACm': 'Total TACm Mined', 'TiUSD 總流通': 'TiUSD Circulation',
    '獎勵池': 'Reward Pool', '出塊速率': 'Block Rate', '我的累計收益': 'My Earnings',
    '全鏈儀表': 'Chain Overview', '最新區塊列表': 'Latest Blocks', '區塊列表': 'Blocks',
    '區塊哈希': 'Block Hash', '交易數': 'Txs', '提議者': 'Proposer', '時間': 'Time', '高度': 'Height',
    '區塊': 'Block', '區塊瀏覽': 'Block Explorer', '交易哈希': 'Tx Hash', '交易': 'Tx',
    '發送方': 'Sender', '接收方': 'Receiver', '金額': 'Amount', '手續費': 'Fee', '備註': 'Memo', '狀態': 'Status',
    '載入中': 'Loading', '失敗': 'Failed', '成功': 'Success', '已確認': 'Confirmed', '待確認': 'Pending',
    '在線': 'Online', '離線': 'Offline', '在線挖礦中': 'Mining Online', '開機挖礦': 'Start Mining', '開機': 'Start',
    '停止挖礦': 'Stop Mining', '停止': 'Stop', '算力': 'Hashrate', '算力佔比': 'Hashrate Share', '總算力': 'Total Hashrate',
    '全鏈總覽': 'Chain Overview', '全網算力': 'Network Hashrate', '我的算力': 'My Hashrate',
    '維度': 'Difficulty', '礦工排行': 'Miner Ranking', '排行': 'Ranking', '平台': 'Platform',
    '台': 'unit(s)', '目前無在線礦工': 'No miners online', '無在線礦工': 'No miners online',
    '在線礦工': 'Online Miners', '礦工': 'Miner', '線上': 'Online',
    'Ai 智能鏈': 'Ai Smart Chain', '穩定幣': 'Stablecoin', '原生幣': 'Native Coin',
    '分鐘前': 'min ago', '小時前': 'h ago', '天前': 'd ago', '剛剛': 'just now',
    '返回': 'Back', '鏈頂': 'Height', '難度': 'Difficulty', '私鑰': 'Private Key',
    '綠燈': 'Active', '灰燈': 'Idle', '綠點': 'Online', '邀請碼': 'Invite Code', '每邀請': 'Per Invite',
    '效率圓環': 'Efficiency Ring', '圓環': 'Ring', '訂單': 'Order', '餘額': 'Balance',
    '費用': 'Fee', '兌換': 'Swap', '快速兌換': 'Quick Swap', '限價': 'Limit', '市價': 'Market',
    '掛單': 'Order', '撤單': 'Cancel', '撮合': 'Match', '點差': 'Spread', '頁籤': 'Tab',
    '運行中': 'Running', '已啟動': 'Started', '已撤單': 'Cancelled', '處理中': 'Processing', '投放中': 'Active',
    '說明': 'Info', '機制': 'Mechanism', '來源': 'Source', '操作': 'Action', '用途': 'Purpose',
    '費率': 'Rate', '取款': 'Withdraw', '領取': 'Claim', '移除': 'Remove', '抵押': 'Collateral',
    '還款': 'Repay', '贖回': 'Redeem', '剩餘': 'Remaining', '下單': 'Place Order',
    '獲得': 'Earn', '最多': 'Max', '帳戶': 'Account', '輸入': 'Enter', '貼上': 'Paste',
    '無效': 'Invalid', '產生': 'Generate', '送出中': 'Sending', '送出': 'Send',
    '合計': 'Total', '實付': 'Paid', '發文': 'Post', '想法': 'Idea', '商品': 'Item',
    '轉入': 'Transfer In', '收幣': 'Receive Coins', '法幣': 'Fiat', '再查': 'Retry', '展開': 'Expand',
    '支持': 'Support', '我的': 'My', '切換': 'Switch', '完整': 'Full', '發行': 'Issuance',
    '年息': 'Annual Interest', '分潤': 'Profit Share', '挹注比例': 'Contribution Ratio',
    '記憶體池': 'Mempool', '已最終化': 'Finalized', '無法連接': 'Cannot Connect',
    '清算價': 'Liquidation Price', '已結清': 'Settled', '應計利息': 'Accrued Interest',
    '尚無貸款': 'No Loans', '項目發行': 'Project Launch', '最大可借': 'Max Borrow',
    '尚無分錄': 'No Entries', '網路錯誤': 'Network Error', '估值對映': 'Valuation',
    '鏈上機制': 'On-chain', '請先貼上': 'Paste First', '移除重複': 'Remove Duplicates',
    '換裝置時用下方': 'Use below when changing devices', '鏈上轉帳': 'On-chain Transfer',
    '一鍵轉帳': 'One-click Transfer', '付款投放': 'Paid Promotion', '來發第一篇吧': 'Be the first to post',
    '內容不可為空': 'Content cannot be empty', '直連不緩存': 'Direct No-cache',
    '請稍後再試': 'Try Again Later', '請轉至': 'Go to', '對手方': 'Counterparty',
    '創世': 'Genesis', '發出': 'Sent', '收入': 'Received', '至少': 'At least',
    '託管': 'Escrow', '轉帳費由託管帳戶承擔': 'Transfer fee paid by escrow',
    '含鏈上轉帳費': 'Includes on-chain fee', '池內可用資金': 'Pool available funds',
    '流動性用途': 'Liquidity use', '報價更優惠': 'Better quotes', '市價可留空': 'Market price optional',
    '出塊與獎勵機制': 'Block & Reward Mechanism', '原本方式': 'original mode',
    '個人資產': 'personal assets', '僅顯示': 'shows only', '總覽': 'Overview',
    '資產說明': 'Assets Info', '錢包同步區塊': 'Wallet synced to Block', '區塊瀏覽器': 'Block Explorer',
    '主網': 'Mainnet', '主網資產': 'Mainnet Assets', '收款地址': 'Recipient Address', '付款地址': 'Payment Address',
    '同步': 'Sync', '估值': 'Valuation', '自動鑄造': 'auto-mint', '鏈上機制': 'on-chain mechanism',
    '供給層': 'supply-layer', '結算': 'settlement', '入帳': 'credit', '禁止': 'prohibited',
    '發行或銷毀': 'issuance or burning', '私自': 'private', '銷毀': 'burn', '最近': 'Recent', '尚無': 'No ', '塊': ' blocks', '每出塊': 'Per Block',
    '最近成交': 'Recent Trades', '區塊交易': 'Block Tx', '我的算力佔比': 'My Hashrate Share',
    '最終化': 'Finalized', '上一區塊': 'Prev Block', '上一': 'Prev ', 'Merkle根': 'Merkle Root', 'Merkle 根': 'Merkle Root',
    '動態難度': 'Dynamic Difficulty', '線上礦工排行': 'Online Miner Ranking', '線上礦工': 'Online Miner', '排行': 'Ranking',
    '地址': 'Address', '節點': 'Node', '帳號': 'Account', '密碼': 'Password', 'Email': 'Email',
    '請輸入帳號': 'Please enter account', '請填寫帳號與金額': 'Please fill in account and amount',
    '請填寫帳號與數量': 'Please fill in account and quantity', '請輸入密碼': 'Please enter password',
    '至少 6 碼': 'At least 6 characters', '請選擇資產並輸入金額': 'Select asset and enter amount',
    '確認轉帳': 'Confirm Transfer', '站內轉帳': 'Internal Transfer', '轉帳手續費': 'Transfer Fee',
    '鏈上費': 'On-chain Fee', '鏈上手續費': 'On-chain Fee', '可用餘額': 'Available Balance', '總資產': 'Total Assets',
    '訪客': 'Guest', '訪客獨立地址': 'Guest Address', '訪客私鑰備份': 'Guest Key Backup',
    '訪客不再共用節點地址': 'Guests no longer share node address', '請妥善保管': 'Keep it safe',
    '首次由伺服器生成': 'First generated by server', '恢復': 'Restore', '備份': 'Backup', '選填': 'Optional',
    '付款地址': 'Payment Address', '複製': 'Copy', '刷新': 'Refresh', '關閉': 'Close', '確認': 'Confirm',
    '資產': 'Assets', '資產餘額': 'Asset Balance', '總流通': 'Circulation', '流通': 'Circulation',
    '最近帳本流': 'Recent Ledger', '審計分錄': 'Audit Entries', '變動': 'Change', '類型': 'Type',
    '數量': 'Amount', '預算': 'Budget', '價值': 'Value', '價格': 'Price', '方向': 'Side',
    '市場': 'Market', '買入': 'Buy', '賣出': 'Sell', '購買': 'Buy', '出售': 'Sell', '查詢': 'Search',
    '選擇': 'Select', '選擇交易對': 'Select Pair', '選擇資產': 'Select Asset', '選擇幣種': 'Select Coin',
    '交易對': 'Pair', '最新': 'Latest', '手續費(taker)': 'Fee (taker)', '委託': 'Order', '我的訂單': 'My Orders',
    '我的廣告': 'My Ads', '發佈廣告': 'Post Ad', '發佈失敗': 'Post Failed', '下單失敗': 'Order Failed',
    '查詢廣告': 'Search Ads', '我要買': 'I want to buy', '我要賣': 'I want to sell',
    '我要買（收幣）': 'Buy (receive coins)', '我要賣（售幣）': 'Sell (send coins)',
    '銀行轉帳': 'Bank Transfer', '支付寶': 'Alipay', '微信支付': 'WeChat Pay', '街口支付': 'JKOPay', 'LINE Pay': 'LINE Pay',
    '限額': 'Limit', '完成': 'Completed', '評分': 'Rating', '支付': 'Payment', '單價': 'Unit Price',
    '最低金額': 'Min Amount', '最高金額': 'Max Amount', '尚無訂單': 'No orders yet', '尚未發佈廣告': 'No ads yet',
    '待付款': 'Pending Payment', '待放行': 'Pending Release', '已完成': 'Completed', '已取消': 'Cancelled',
    '申訴中': 'Disputed', '申訴': 'Dispute', '確認已付款': 'Confirm Payment', '取消': 'Cancel',
    '放行幣': 'Release Coins', '賣家放行': 'Seller Release', '法幣付款': 'Fiat Payment', '已付款': 'Paid',
    'C2C 場外交易': 'C2C OTC Trading', '買幣 / 賣幣': 'Buy / Sell', '廣告主擔保交易': 'Advertiser-guaranteed trades',
    '法幣直接買賣加密貨幣': 'Buy & sell crypto with fiat',
    '支持 TWD/CNY/USD/HKD/JPY': 'Support TWD/CNY/USD/HKD/JPY',
    '流動性挖礦': 'Liquidity Mining', '借貸市場': 'Lending Market', 'IDO 發行': 'IDO Launch', '收益金庫': 'Vault',
    '流動性池': 'Liquidity Pools', '儲備': 'Reserve', '總LP': 'Total LP', 'APR': 'APR', '動態': 'Dynamic',
    '添加流動性': 'Add Liquidity', '加入流動性': 'Add Liquidity', '我的頭寸': 'My Positions',
    'LP 份額即池內權益': 'LP share equals pool equity', '身份': 'Identity', '讀取': 'Load', '未連接': 'Not Connected',
    '開頭地址': 'address starting with', '開頭': 'starting with', '地址為身份操作': 'Address as identity',
    '存款': 'Deposit', '借款': 'Borrow', '抵押率': 'Collateral Ratio', '總存款': 'Total Deposits', '總借款': 'Total Borrows',
    '存入': 'Deposit', '取出': 'Withdraw', '借出': 'Borrow', '抵押資產': 'Collateral Asset', '抵押數量': 'Collateral Amount',
    '借款資產': 'Borrow Asset', '借款數量': 'Borrow Amount', '我的存款': 'My Deposits', '我的貸款': 'My Loans',
    '每日結息計入市場': 'Daily interest credited to market', '全額還款釋放抵押': 'Full repayment releases collateral',
    '認購': 'Subscribe', '我的認購': 'My Subscriptions', '金庫頭寸': 'Vault Positions', '策略自動復投': 'Auto-compound strategy',
    '流動性挖礦與借貸': 'Liquidity Mining & Lending', '獎勵': 'Reward', '待領獎勵': 'Pending Reward',
    '投入': 'Deposited', '加入': 'Add', '市場': 'Market', '最新區塊': 'Latest Blocks', '出塊': 'Block',
    '節點': 'Nodes', '節點': 'Node', '共識': 'Consensus', '質押': 'Stake', '鎖倉': 'Lock',
    '社區': 'Community', '貼文': 'Posts', '成員': 'Members', '市集': 'Marketplace', '廣告': 'Ads',
    '讚': 'Like', '留言': 'Comment', '分享': 'Share', '發佈': 'Post', '發布': 'Post', '貼文內容': 'Post content',
    '回覆': 'Reply', '已售出': 'Sold', '上架': 'List', '下架': 'Delist', '暫停': 'Pause', '啟用': 'Enable',
    '售': 'Sell', '買': 'Buy', '賣': 'Sell', '序號': 'Order No', '狀態': 'Status',
    '總額': 'Total', '最新成交': 'Recent Trades', '成交': 'Trades', '深度': 'Depth', '訂單簿': 'Order Book',
    '資金池': 'Pool', '閃兌': 'Swap', '自動交易': 'Auto Trading', '機器人': 'Bot', '交易機器人': 'Trading Bot',
    '我的資產': 'My Assets', '資產餘額': 'Asset Balance', '入金': 'Deposit', '提現': 'Withdraw', '劃轉': 'Transfer',
    '交易所資金池': 'Exchange Pool', '預設同源': 'Default same-origin', '同埠合併模式': 'Same-port merge mode',
    '端口 覆蓋': 'Port override', 'RPC 即 web': 'RPC is web', '仍可用': 'still available', '雙保險': 'double insurance',
    '連結': 'Link', '鏈接': 'Link', '網絡': 'Network', '節點狀態': 'Node Status', '總節點': 'Total Nodes',
    '出塊中': 'Mining Blocks', '同步中': 'Syncing', '停止中': 'Stopped', '未知': 'Unknown',
    '手動': 'Manual', '自動': 'Auto', '開啟': 'On', '關閉': 'Off',
    '請稍候': 'Please wait', '操作失敗': 'Operation failed', '請輸入數量': 'Please enter amount',
    '請填寫完整價格與限額': 'Please fill in price and limits', '已創建訂單': 'Order created',
    '確認已完成法幣付款': 'Confirm fiat payment completed?', '已付款請確認': 'Paid, please confirm',
    '載入失敗': 'Load failed', '暫無數據': 'No data', '目前無有效廣告': 'No active ads',
    '尚未發佈廣告': 'No ads published', '無更多': 'No more', '所有權': 'Ownership', '總供應量': 'Total Supply',
    '已挖出': 'Mined', '年衰減': 'Annual Decay', '發行量': 'Issuance', '上限': 'Cap',
    '跨鏈': 'Cross-chain', '跨鏈橋': 'Bridge', '鎖定': 'Locked', '鑄造': 'Minted', '銷毀': 'Burned',
    'L2': 'L2', 'Rollup': 'Rollup', '欺詐證明': 'Fraud Proof', '驗證': 'Verify', '輕客戶端': 'Light Client',
    '挖礦機': 'Miner', '礦機': 'Miner', '我的挖礦機': 'My Miner', '挖礦收益': 'Mining Rewards',
    '效率圈': 'Efficiency Ring', '全網挖礦速率': 'Network Mining Rate', '每分鐘出塊': 'Blocks per minute',
    '收益': 'Rewards', '歷史收益': 'History', '今日收益': 'Today', '累計收益': 'Total Rewards',
    '個人產出': 'My Output', '全鏈產出': 'Chain Output', '開始': 'Start', '結束': 'End', '暫停': 'Pause',
    '← 返回': '← Back',
    '尚無交易': 'No transactions',
    '相關交易（': 'Related Txs (',
    '出塊時間': 'Block Time',
    '區塊交易（': 'Block Txs (',
    '本區塊無交易': 'No transactions in this block',
    '交易序號': 'Nonce',
    '交易金額': 'Amount',
    '公鑰': 'Public Key',
    '合約類型': 'Contract Type',
    '所在區塊': 'Block',
    '簽名': 'Signature',
    '簽名驗證資訊': 'Signature Verification',
    'Gas 使用量': 'Gas Used',
    '事件日誌': 'Event Logs',
    '合約地址': 'Contract Address',
    '← 返回區塊 #': '← Back to Block #',
    '（coinbase/創世）': ' (coinbase/genesis)',
    '邀請人地址（選填）': 'Inviter Address (optional)',
    '填寫邀請人地址＝邀請人礦機算力 +0.01 CPU／+0.02 GPU': 'Fill in the inviter address = inviter miner +0.01 CPU / +0.02 GPU',
    '加入網絡': 'Join Network',
    '最終化 #': 'Finalized #',
    '節點監控': 'Node Monitor',
    '鏈頂 #': 'Height #',
    '我的 C2C 訂單': 'My C2C Orders',
    '發佈 C2C 廣告': 'Post C2C Ad',
    '法幣直接買賣加密貨幣 · 廣告主擔保交易 · 支持 TWD/CNY/USD/HKD/JPY': 'Buy & sell crypto with fiat · Advertiser-guaranteed trades · TWD/CNY/USD/HKD/JPY',
    'TAC 社群': 'TAC Community',
  '建立貼文': 'Create post',
  '上傳圖片/影片': 'Upload photo/video',
  '市集商品': 'Marketplace',
  '發布': 'Publish',
  '在想些什麼？': "What's on your mind?",
  '請先上傳圖片/影片': 'Please upload a photo/video first',
  '內容不可為空': 'Content cannot be empty',
  '市集請填價格': 'Please enter a marketplace price',
  '發布失敗': 'Publish failed',
  '上傳失敗': 'Upload failed',
  '請先在上方輸入你的錢包地址（tx0 開頭）': 'Please enter your wallet address above (starts with tx0)',
  '觀看': 'watching',
  '返回': 'Back',
  '切換': 'Switch',
  '個人主頁': 'Profile',
  '你的朋友': 'Your Friends',
  '追蹤你的人': 'Followers',
  '建議（可能認識）': 'Suggested',
  '人在線': 'online',
  '我的錢包': 'My Wallet',
  '我的礦機': 'My Miner',
  '交易所': 'Exchange',
  '我的貼文': 'My Posts',
  '鏈上資產': 'On-chain Assets',
  '尚未有影片，快來發佈第一支影片吧！': 'No videos yet. Be the first to post one!',
  '則貼文': 'posts',
  '追蹤中': 'Following',
  '追蹤者': 'followers',
  '新增朋友': 'Add Friend',
  '尚未追蹤任何人': 'Not following anyone yet',
  '尚無人追蹤你': 'No followers yet',
  '暫無建議': 'No suggestions yet',
  '可能認識的朋友': 'People you may know',
  '按讚了你的貼文': 'liked your post',
  '留言了你的貼文': 'commented on your post',
  '開始追蹤你': 'started following you',
  '獲得鏈上互動積分獎勵': 'You earned on-chain engagement points',
  '尚無通知': 'No notifications yet',
  '影片': 'Video',
  '市集': 'Market',
  '動態': 'Post',
'開啟影片': 'Open Video',
    '發布商品': 'List Item', '上架': 'Publish', '確認購買': 'Confirm Purchase', '確認付款（一鍵轉帳）': 'Confirm & Pay (one-click)',
    '評價市集商品': 'Rate Item', '評分（1-5）': 'Rating (1-5)', '送出評價': 'Submit Review',
    '建立廣告': 'Create Ad', '投放位置': 'Placement', '資訊流插播': 'In-feed ads',
    '總預算': 'Total Budget', '每日預算': 'Daily Budget', '點擊跳轉網址': 'Click-through URL',
    '發佈廣告（付款後投放）': 'Publish Ad (starts after payment)', '我的廣告': 'My Ads', '了解更多': 'Learn More',
    '已售出': 'Sold', '購買': 'Buy', '評價': 'Review', '賣家': 'Seller', '鏈上費': 'Chain Fee',
    '合計': 'Total', '處理中…': 'Processing…', '商品 ID': 'Item ID',
    '市集尚無商品': 'No items in marketplace yet', '載入失敗': 'Load failed', '請先輸入你的錢包地址': 'Enter your wallet address first',
    '以錢包中心為準': 'From wallet center', '請先於錢包中心選擇錢包地址（tx0 開頭）': 'Please select your wallet address (tx0) in the wallet center first', '請先於錢包中心選擇錢包地址': 'Please select your wallet address in the wallet center first',
    '請填寫商品名稱與價格': 'Fill in item name and price', '上架失敗：': 'Publish failed: ',
    '廣告已建立（ID #': 'Ad created (ID #', '請填寫廣告標題與總預算': 'Fill in ad title and total budget', '發佈失敗：': 'Publish failed: ',
    '投放中': 'Active', '待付款': 'Awaiting Payment', '付款投放': 'Pay & Activate',
    '動態流': 'Feed', '影片流': 'Video Feed', '動態流＋影片流': 'Feed + Video Feed',
    '每日': 'Daily', '曝光': 'Impressions', '點擊': 'Clicks', '尚無廣告': 'No ads yet',
    '確認支付廣告預算並開始投放？': 'Confirm paying ad budget and starting delivery?', '廣告已投放（ID #': 'Ad activated (ID #',
    '付款失敗：': 'Payment failed: ', '按「了解更多」前往': 'Opens on "Learn More"',
  '積分': 'points',
  '尚無動態，來發第一篇吧！': 'No posts yet. Be the first to post!',
  '請先輸入你的錢包地址（tx0 開頭）': 'Please enter your wallet address first (starts with tx0)',
    '市集商品': 'Marketplace Items',
    '礦機內建社群 · 動態牆／市集／廣告池': 'Built-in Community · Feed / Marketplace / Ad Pool',
    '社群身分＝TAC 錢包地址（tx0 開頭）。發文、按讚、留言、市集與廣告皆以該地址記錄。': 'Community identity = TAC wallet address (tx0). Posts, likes, comments, marketplace and ads are recorded to this address.',
    ' 我的身份': ' My Identity',
    ' 快捷入金至交易所': ' Quick Deposit to Exchange',
    ' 發佈動態 / 商品': ' Post Update / Item',
    ' 廣告池': ' Ad Pool',
    ' 市集': ' Marketplace',
    ' 購買商品（TACM 鏈上轉帳）': ' Buy Item (TACM on-chain transfer)',
    'TAC Ai 智能鏈 ·': 'TAC Ai Smart Chain ·',
    '管理儀表板': 'Management Dashboard',
    '10% 年息、全額還款釋放抵押': '10% APR, full repayment releases collateral',
    'APR 動態': 'APR Dynamic',
    'IDO 項目發行': 'IDO Project Launch',
    '以 USDT 認購': 'Subscribe with USDT',
    '以地址為身份操作 DeFi；資產進出即時上鏈記帳（費率：TACm 2%／TiUSD 0.5%／USDT 1.25%）。': 'Use your address as identity; assets move on-chain instantly (fee: TACm 2% / TiUSD 0.5% / USDT 1.25%).',
    '借款 APR': 'Borrow APR',
    '儲備 T0': 'Reserve T0',
    '儲備 T1': 'Reserve T1',
    '存款 APR': 'Deposit APR',
    '存款／取款': 'Deposit / Withdraw',
    '我的金庫頭寸': 'My Vault Positions',
    '抵押借款（抵押資產 × 抵押率 = 最大可借）': 'Collateralized Borrow (Collateral × Ratio = Max Borrow)',
    '抵押率＝可借上限': 'Collateral ratio = borrow cap',
    '收益聚合器': 'Yield Aggregator',
    '總 LP': 'Total LP',
    '選擇…': 'Select…',
    '選擇交易對…': 'Select pair…',
    '選擇資產…': 'Select asset…',
    '＋ 添加流動性': '+ Add Liquidity',
    ' 收益金庫': ' Vault',
    ' 借貸市場': ' Lending Market',
    ' 流動性挖礦': ' Liquidity Mining',
    ' 身份（錢包地址）': ' Identity (wallet address)',
    ' IDO 發行': ' IDO Launch',
    '未選中農場': 'No farm selected',
    'TiUSD 報價更優惠：taker 0.5%': 'TiUSD better quotes: taker 0.5%',
    'coinbase 15% 自動挹注': 'coinbase 15% auto-contribution',
    '交易市場': 'Trading Market',
    '交易所手續費': 'Exchange Fee',
    '交易所流動性／閃兌備付／營運資金，可 withdraw 至指定地址': 'Exchange liquidity / swap reserve / working capital; withdrawable to a designated address',
    '交易所資產餘額': 'Exchange Asset Balance',
    '價格(USDT)': 'Price (USDT)',
    '入金（鏈上→交易所）': 'Deposit (chain → exchange)',
    '入金：扣鏈上錢包（含鏈上轉帳費）後存入交易所帳戶': 'Deposit: deduct on-chain wallet (incl. on-chain fee) then credit exchange account',
    '區塊 coinbase': 'Block coinbase',
    '可用': 'Available',
    '啟動機器人': 'Start Bot',
    '已成交': 'Filled',
    '提現（交易所→鏈上）': 'Withdraw (exchange → chain)',
    '提現：扣交易所餘額後轉回鏈上錢包（轉帳費由託管帳戶承擔）': 'Withdraw: deduct exchange balance then send back to on-chain wallet (fee paid by escrow)',
    '撮合／閃兌手續費結算入池（/api/rewardpool/topup）': 'Match/swap fees settled into pool (/api/rewardpool/topup)',
    '數量(TACM)': 'Amount (TACM)',
    '查詢帳號': 'Search Account',
    '每出 1 塊，補貼 10 TACm 的 15% 自動挹注本池': 'Per block, 15% of 10 TACm auto-contributes to this pool',
    '池內可用資金（TACm，作交易所運營/流動性用途）': 'Pool available funds (TACm, for exchange ops/liquidity)',
    '累計注入': 'Total Contributed',
    '累計領用': 'Total Withdrawn',
    '自動交易機器人': 'Auto Trading Bot',
    '資金流入／用途說明': 'Fund Inflow / Usage',
    '資金用途': 'Fund Usage',
    '閃兌 · 快速兌換': 'Swap · Quick Exchange',
    ' 交易所資金池（獎勵池）': ' Exchange Funding Pool (Reward Pool)',
    '哈希': 'Hash',
    '尚無區塊': 'No blocks',
    '搜尋': 'Search',
    '最新交易': 'Latest Transactions',
    '目標': 'Target',
    '節點位址': 'Node Address',
    '運行時間': 'Uptime',
    '金額 TACm': 'Amount TACm',
    ' 全鏈統計': ' Chain Stats',
    ' 鏈上搜尋': ' On-Chain Search',
    ' 全鏈儀表': ' Chain Overview',
    '10GB 以上（鏈資料會持續增長）': '10GB+ (chain data grows continuously)',
    '2 秒': '2 sec',
    'Linux / macOS（需 Go 1.22+）': 'Linux / macOS (Go 1.22+ required)',
    'PoT 動態難度 + BFT 投票': 'PoT dynamic difficulty + BFT voting',
    'coinbase 分潤（池 + 礦工）': 'coinbase split (pool + miners)',
    '✅ 驗證與挖礦': '✅ Verify & Mine',
    '出塊獎勵': 'Block Reward',
    '區塊時間': 'Block Time',
    '建議 2GB RAM 以上': '2GB+ RAM recommended',
    '磁碟': 'Disk',
    '種子節點': 'Seed Node',
    '系統': 'System',
    '網路': 'Network',
    '與獨立資料目錄，與主網完全分離，可放心試跑。': 'and a separate data dir, fully isolated from mainnet — safe to trial.',
    '複製指令': 'Copy Command',
    '要求': 'Requirements',
    '角色': 'Role',
    '記憶體': 'Memory',
    '連上創世節點完成握手，即開始同步主網。': 'Handshake with the genesis node completes, then sync starts from mainnet.',
    '鏈 ID': 'Chain ID',
    '開放 TCP 8080 埠（P2P + RPC 同埠合併）': 'Open TCP 8080 (P2P + RPC same-port merge)',
    '項目': 'Requirement',
    '首次啟動自動從 seed 節點同步全鏈': 'First start auto-syncs full chain from seed node',
    ' 網絡概況': ' Network Overview',
    ' 一鍵加入指令': ' One-Click Join Command',
    ' 節點要求': ' Node Requirements',
    ' 公開 Seed 列表': ' Public Seed List',
    ' 加入 TAC Ai 智能鏈主網': ' Join TAC Ai Smart Chain Mainnet',
    'CPU 算力': 'CPU Hashrate',
    'GPU 算力': 'GPU Hashrate',
    '——每邀請 1 名會員註冊，': '— Per member registered with your invite,',
    '⛏ 出塊與獎勵機制（原本方式）': '⛏ Block & Reward Mechanism (original mode)',
    '（原本方式）': ' (original mode)',
    '⛏ 我的礦機': '⛏ My Miner',
    '・基礎': '· Base',
    '・提升方式：': '· How to boost:',
    '・邀請碼＝你的錢包地址，分享給朋友於註冊時填寫即可。': '· Invite code = your wallet address; share it for friends to fill in at registration.',
    ' · 總算力': ' · Total Hashrate',
    '推薦註冊': 'Referral Registration',
    '算力設定（固定不可改）': 'Hashrate Setting (fixed, immutable)',
    ' 全鏈總覽': ' Chain Overview',
    ' 在線礦工排行（': ' Online Miner Ranking (',
    '待打包': 'Pending',
    '節點 ID': 'Node ID',
    '運行': 'Running',
    ' 本節點': ' This Node',
    ' 節點監控（全網統一視界）': ' Node Monitor (Unified Network View)',
    ' 鏈上在線礦工（': ' On-Chain Online Miners (',
    '台 · 算力瓜分視角）': ' unit(s) · hashrate split)',
    'TACm 原生幣': 'TACm Native Coin',
    'TACm（手續費 2%）': 'TACm (fee 2%)',
    'TiUSD 穩定幣': 'TiUSD Stablecoin',
    'TiUSD（手續費 0.5%）': 'TiUSD (fee 0.5%)',
    'USDT（手續費 1.25%）': 'USDT (fee 1.25%)',
    '備份私鑰': 'Backup Private Key',
    '我的地址': 'My Address',
    '暫無分錄。挖礦出塊後 coinbase 獎勵會自動入帳。': 'No entries yet. Coinbase rewards auto-credit after each block.',
    '最近帳本流（審計分錄）': 'Recent Ledger (Audit)',
    '複製私鑰': 'Copy Private Key',
    '訪客錢包（未登入）': 'Guest Wallet (not signed in)',
    '請妥善保管，換裝置時用下方「恢復」取回同一地址': 'Keep it safe; use "Restore" below to recover the same address on another device',
    '≈ 獲得 ': '≈ Receive ',
    '✅ 借款成功 #': '✅ Borrow success #',
    '✅ 取款成功 ': '✅ Withdraw success ',
    '✅ 存款成功 ': '✅ Deposit success ',
    '✅ 添加成功 · 獲得 LP ': '✅ Added · LP received ',
    '✅ 確認付款（一鍵轉帳）': '✅ Confirm Payment (one-click transfer)',
    '停止失敗：': 'Stop failed: ',
    '已複製 ✓': 'Copied ✓',
    '已轉入交易所 ': 'Transferred to exchange ',
    '市集請填價格': 'Marketplace: enter price',
    '搜尋失敗，請稍後再試': 'Search failed, try again later',
    '搜尋服務暫時不可用': 'Search service temporarily unavailable',
    '更新於 ': 'Updated at ',
    '未找到：': 'Not found: ',
    '無法取得全網狀態': 'Unable to fetch network status',
    '無法取得審計結果': 'Unable to fetch audit result',
    '無法連接 RPC（': 'Cannot connect RPC (',
    '發布失敗：': 'Post failed: ',
    '社群資料載入失敗（': 'Community data load failed (',
    '聚合狀態讀取失敗': 'Failed to read aggregator state',
    '訪客地址取得失敗（': 'Guest address fetch failed (',
    '請先輸入你的錢包地址（tx0 開頭）': 'Enter your wallet address first (tx0)',
    '請填寫抵押與借款': 'Fill in collateral and borrow',
    '請輸入金額': 'Enter an amount',
    '請選擇池並輸入兩種代幣數量': 'Select a pool and enter both token amounts',
    '轉入失敗：': 'Transfer-in failed: ',
    '開機失敗：': 'Start failed: ',
    ' 鏈上驗證：暫不可用': ' On-chain verification: unavailable',
    'JSON 編碼失敗': 'JSON encoding failed',
    '借款成功': 'Borrow success',
    '參數格式錯誤': 'Invalid parameter format',
    '取出成功': 'Withdrawn',
    '取款成功': 'Withdraw success',
    '存入成功': 'Deposited',
    '存款成功': 'Deposit success',
    '已提交申訴，客服將盡快處理': 'Dispute submitted, support will handle shortly',
    '已放行，交易完成': 'Released, trade completed',
    '已確認付款，等待賣家放行': 'Payment confirmed, waiting for seller release',
    '廣告創建成功': 'Ad created',
    '廣告狀態已更新': 'Ad status updated',
    '復投成功': 'Re-compounded',
    '添加流動性成功': 'Liquidity added',
    '移除流動性成功': 'Liquidity removed',
    '訂單創建成功': 'Order created',
    '訂單已取消': 'Order cancelled',
    '認購成功': 'Subscribed',
    '還款成功': 'Repaid',
    '領取成功': 'Claimed',
    '領取獎勵成功': 'Rewards claimed',
    ' 加入 TAC Ai 智能鏈主網': ' Join TAC Ai Smart Chain Mainnet',
    '任何一台伺服器都可成為節點，與創世節點同步同一條鏈、共享全網狀態；內建礦機可額外挖礦瓜分出塊獎勵。節點之間嚴格遵循同一套出塊獎勵規則，任何入口看到的鏈上資料與在線礦工完全一致。': 'Any server can become a node: sync the same chain with the genesis node, share global status; the built-in miner can mine and share block rewards. All nodes follow the same reward rules — every entry shows identical on-chain data and online miners.',
    ' 網絡概況': ' Network Overview',
    '鏈 ID': 'Chain ID',
    'PoT 動態難度 + BFT 投票': 'PoT Dynamic Difficulty + BFT Voting',
    '出塊獎勵': 'Block Reward',
    'coinbase 分潤（池 + 礦工）': 'coinbase split (pool + miners)',
    '種子節點': 'Seed Nodes',
    '區塊時間': 'Block Time',
    '2 秒': '2 sec',
    ' 公開 Seed 列表': ' Public Seed List',
    'testnet 為實驗網段，無固定公開 seed——由創世節點以 -p2p-seed 自行播報': 'Testnet is experimental with no fixed public seed — propagated by the genesis node via -p2p-seed',
    '尚無公開 seed': 'No public seed yet',
    ' 一鍵加入指令': ' One-click Join Commands',
    '複製指令': 'Copy Command',
    '&lt;你的公網IP&gt;': '&lt;your public IP&gt;',
    '把 ': 'Replace ',
    ' 與 ': ' and ',
    ' 換成實際值後逐行執行。testnet 使用獨立鏈 ID ': ' with real values, run line by line. Testnet uses a separate chain ID ',
    ' 與獨立資料目錄，與主網完全分離，可放心試跑。': ' and a separate data directory, fully isolated from mainnet — safe to try.',
    '換成你自己的 IP 後逐行執行。啟動後以 ': 'with your own IP, run line by line. After start, use ',
    '連上創世節點完成握手，即開始同步主網。': 'to connect to the genesis node, complete the handshake, and start syncing mainnet.',
    ' 節點要求': ' Node Requirements',
    '記憶體': 'Memory',
    '建議 2GB RAM 以上': '2GB+ RAM recommended',
    '10GB 以上（鏈資料會持續增長）': '10GB+ (chain data keeps growing)',
    '開放 TCP 8080 埠（P2P + RPC 同埠合併）': 'Open TCP 8080 (P2P + RPC share one port)',
    '首次啟動自動從 seed 節點同步全鏈': 'Auto-syncs full chain from seed nodes on first start',
    '✅ 驗證與挖礦': '✅ Verify & Mine',
    '啟動後在網頁「挖礦」頁開啟礦機即可參與出塊：收益按鏈上 coinbase 分潤規則發放（池 + 礦工），開機/關機狀態經心跳交易寫入鏈上，任何入口看到你的在線狀態一致。': 'Open the miner on the web Mining page to start producing blocks: rewards follow the on-chain coinbase split (pool + miners); start/stop status is written on-chain via heartbeat txs — every entry shows your consistent online status.',
    '標題': 'Title',
    '描述': 'Description',
    '參數': 'Parameter',
    '數值': 'Value',
    '你的地址': 'Your Address',
    '全部提案': 'All Proposals',
    '間隔（秒）': 'interval (s)',
    'TxFee基準（萬分比，2%=200）': 'TxFee base (bp, 2%=200)',
    '下限佔 TACM 總供應比例（萬分比）': 'min share of total TACM supply (bp)',
    '節點監控 · TAC Network': 'Node Monitor · TAC Network',
    '鏈高': 'Block Height',
    '審計塊數': 'Audited Blocks',
    '獎勵池餘額': 'Reward Pool Balance',
    '鏈上在線礦工': 'On-chain Online Miners',
    '交易筆數（重放）': 'Txs (replay)',
    '合約交易': 'Contract Txs',
    '賬本守恆': 'Ledger Conservation',
    '（每 10 秒自動刷新）': ' (auto-refresh every 10s)',
    '台 · 總算力': 'rigs · Total Hashrate',
    '0 個代幣': '0 tokens',
    'TAC Ai 智能鏈 · ': 'TAC Ai Smart Chain · ',
    '管理儀表板': 'Admin Dashboard',
    'No 農場': 'No Farms',
    '從': 'From',
    '到': 'To',
    '農場': 'Farm',
    '加入網絡 · TAC Node': 'Join Network · TAC Node',
    '全網節點': 'Global Nodes',
    '目前無在線礦工（鏈上瓜分視角，最近 60 塊）': 'No miners online (on-chain split view, last 60 blocks)',
    '目標出塊間隔（秒）': 'Target block interval (s)',
    '基礎 PoT 難度': 'Base PoT Difficulty',
    'TACm 交易手續費基準（萬分比，2%=200）': 'TACm Tx Fee Base (bps, 2%=200)',
    'TiUSD 流通下限佔 TACM 總供應比例（萬分比）': 'TiUSD Circulation Floor (% of TACM total supply, bps)',
    '# 瀏覽器開啟 http://127.0.0.1:8080/explorer 查看你同步的鏈': '# Open http://127.0.0.1:8080/explorer in your browser to view your synced chain',
    '全網同步': 'Network Synced',
    '所有節點鏈高一致（#': 'All nodes at the same height (#',
    '鏈高不一致': 'Height Mismatch',
    '各節點：': 'Per node: ',
    '（等待同步或檢查節點）': ' (waiting for sync or check nodes)',
    '你的公網IP': 'your public IP',
    '基礎難度 (difficulty)': 'Base Difficulty (difficulty)',
    '手續費基準 (tx_fee_bps)': 'Tx Fee Base (tx_fee_bps)',
    ' <b style="color:#0ecb81">鏈上驗證：一致</b>': ' <b style="color:#0ecb81">On-chain Verify: Consistent</b>',
    '審計 ': 'Audited ',
    ' 塊 coinbase/pool/split 全過 · 總產出 ': ' blocks coinbase/pool/split all pass · Total mined ',
    ' TACm · 獎勵池挹注 ': ' TACm · Reward Pool fed ',
    ' · 在線礦工 ': ' · Online Miners ',
    ' 台（與 /network 同源）': ' rig(s) (same source as /network)',
    // M73-B 社群鏈上激勵與關注。
    '互動積分與鏈上獎勵': 'Engagement Points & On-Chain Rewards',
    '查詢': 'Check', '切換': 'Switch', '追蹤': 'Follow', '已追蹤': 'Following', '取消追蹤': 'Unfollow',
    '全部動態': 'All Posts', '追蹤中': 'Following', '我的積分：': 'My points: ', ' 分': ' pts',
    '結算週期：每 ': 'Settlement: every ', ' 塊／每期 ': ' blocks / ', ' TACM · ': ' TACM · ',
    '已結算 ': 'settled ', ' 期': ' period(s)',
    '積分排行榜': 'Points Leaderboard', '暫無積分': 'No points yet',
    '立即結算': 'Settle Now', '結算完成，': 'Settled — ', ' 位成員獲得鏈上獎勵': ' member(s) received on-chain rewards',
    '請先輸入錢包地址': 'Enter a wallet address first', '操作失敗：': 'Action failed: ',
    '查詢失敗：': 'Query failed: ', '結算失敗：': 'Settlement failed: ',
    '（與 /network 同源）': ' (same source as /network)',
  
    '1 星 — 很差': '1 star — very poor',
    '100 名': '100 spots',
    '2 個節點時任一離線鏈即暫停': 'with 2 nodes the chain pauses if either goes offline',
    '2 星 — 不佳': '2 stars — poor',
    '3 個以上': '3 or more',
    '3 星 — 普通': '3 stars — average',
    '4 星 — 好': '4 stars — good',
    '45% 節點獎勵': '45% node reward',
    '5 星 — 非常好': '5 stars — excellent',
    'ABI 不是合法 JSON：': 'ABI is not valid JSON:',
    'AI 助手': 'AI Assistant',
    'AI 引擎：': 'AI engine:',
    'BFT 共識需 2/3 節點在線才持續出塊：': 'BFT consensus needs 2/3 nodes online to keep producing blocks:',
    'BSC Bridge（SPV＋多簽）': 'BSC Bridge (SPV + multi-sig)',
    'CDP 市場暫不可用': 'CDP market temporarily unavailable',
    '超額抵押 CDP': 'DAI-style CDP',
    'DEX 兌換': 'DEX Swap',
    'EIP-1559（Base Fee 銷毀）': 'EIP-1559 (Base Fee burn)',
    'EVM 原子性與 gas 資源治理': 'EVM atomicity & gas resource governance',
    'K 線': 'K-line',
    'K 線 · 深度 · 訂單簿 · 下單': 'K-line · Depth · Order book · Trade',
    'L2 Rollup 未啟用': 'L2 Rollup not enabled',
    'L2 高度': 'L2 height',
    'NFT 市場': 'NFT Marketplace',
    'Sponsored · TAC 廣告': 'Sponsored · TAC Ads',
    'TACM 入資金池）': 'TACM into the pool)',
    'TACM 可用': 'TACM available',
    'TACM 請轉至': 'TACM — please transfer to',
    'TACM 鏈上費（已入資金池）': 'TACM on-chain fee (already in pool)',
    'TACM（鏈上費': 'TACM (on-chain fee',
    'TACm / TiUSD / USDT 即時兌換': 'TACm / TiUSD / USDT instant swap',
    'TACm · 債務': 'TACm · debt',
    'TACm · 獎勵池挹注': 'TACm · reward pool top-up',
    'TACm · 總債務': 'TACm · total debt',
    'TACm 參考價 $': 'TACm reference price $',
    'TiUSD 可用': 'TiUSD available',
    'TiUSD 超額抵押 · 抵押 TACm 借 TiUSD': 'TiUSD over-collateralized · stake TACm to borrow TiUSD',
    'USDT 可用': 'USDT available',
    'WIF 無效，無法恢復': 'Invalid WIF, cannot recover',
    'Web3 錢包': 'Web3 Wallet',
    'coinbase 15% 自動挹注 · 獎勵': 'coinbase 15% auto top-up · reward',
    '· 付款後才開始投放（TACM 全額入資金池）': '· ad starts only after payment (all TACM into pool)',
    '· 低曝光廣告優先輪播，曝光／點擊即時計數': '· low-exposure ads rotate first; views/clicks counted live',
    '· 借出': '· lend',
    '· 可清算': '· liquidatable',
    '· 在線礦工': '· miners online',
    '· 待領': '· pending claim',
    '· 應計利息': '· accrued interest',
    '· 我': '· me',
    '· 最佳': '· best',
    '· 未平倉': '· open positions',
    '· 清算價': '· liquidation price',
    '· 獎勵': '· reward',
    '· 穩定費': '· stability fee',
    '· 賣': '· sell',
    '· 點擊': '· clicks',
    '— 點差 —': '— spread —',
    '← 上一頁': '← Prev',
    '← 從列表選擇 NFT 集合': '← pick an NFT collection from the list',
    '① 已付款': '① Paid',
    '① 法幣付款': '① Fiat payment',
    '② 已完成': '② Completed',
    '② 賣家放行': '② Seller releases',
    '○ 離線': '○ Offline',
    '● 在線': '● Online',
    '✓ 已確認收到節點獎勵（近 200 塊入帳': '✓ node reward received (last ~200 blocks)',
    '。啟動後節點品牌控制台＝': '. After start, node console =',
    '。確認已完成法幣付款？': '. Confirm fiat payment done?',
    '。節點越多、出塊越分散，鏈越去中心化（最多': '. More nodes spread block production — the chain gets more decentralized (max',
    '一行一鍵搭建': 'one-line one-click setup',
    '上一頁': 'Prev',
    '上傳失敗：': 'Upload failed:',
    '上鏈中…': 'Submitting to chain…',
    '下一頁': 'Next',
    '下一頁 →': 'Next →',
    '事件': 'Events',
    '互動積分': 'Interaction points',
    '交易/合約層發現異常，需立即檢查': 'Anomaly found at tx/contract layer — check immediately',
    '交易上鏈中…（滑點保護已啟用）': 'Submitting tx… (slippage protection on)',
    '交易序號 Nonce': 'Nonce',
    '交易所手續費帳戶': 'Exchange fee account',
    '交易所資金池（獎勵池）': 'Exchange pool (reward pool)',
    '交易所餘額（TACM）': 'Exchange balance (TACM)',
    '交易紀錄（鏈上）': 'Transaction history (on-chain)',
    '付款成功　實付': 'Payment success  paid',
    '代簽授權': 'Proxy signing authorization',
    '代簽鑄造': 'Proxy signing mint',
    '位成員獲得鏈上獎勵': 'members earned on-chain rewards',
    '個 NFT 集合': 'NFT collections',
    '借出 TiUSD': 'Lend TiUSD',
    '借款成功 #': 'Loan success #',
    '備份助記詞': 'Backup mnemonic',
    '備註：': 'Note:',
    '償還': 'Repay',
    '償還成功 · 剩餘': 'Repaid · remaining',
    '儲存失敗': 'Save failed',
    '兌換失敗': 'Swap failed',
    '兌換成功：': 'Swap success:',
    '入帳筆數（近 200 塊）': 'Payments in (last ~200 blocks)',
    '入金 · 提現 · 交易所帳戶': 'Deposit · Withdraw · Exchange account',
    '入金成功（鏈上手續費': 'Deposit success (on-chain fee',
    '全部': 'All',
    '全量授權狀態:': 'Full approval status:',
    '全鏈帳本重放（ledger audit API）': 'Full-chain ledger replay (ledger audit API)',
    '全鏈統計': 'Chain-wide stats',
    '全鏈賬本一致': 'Ledger consistent chain-wide',
    '兩者': 'both',
    '公開 Seed 列表': 'Public Seed list',
    '公開倉庫網址': 'Public repo URL',
    '凍結': 'Frozen',
    '出塊與獎勵機制（當前機制）': 'Block & reward mechanism (current)',
    '刪除': 'Delete',
    '刪除失敗': 'Delete failed',
    '創節點引導獎勵（限額 50 名）': 'Genesis bootstrap reward (limit 50)',
    '加入 TAC Ai 智能鏈主網': 'Join TAC Ai Smart Chain mainnet',
    '加入網路失敗': 'Join network failed',
    '加成': 'Bonus',
    '加成時權': 'Bonus uptime',
    '助記詞已複製，請妥善保管': 'Mnemonic copied — keep it safe',
    '助記詞無效': 'Invalid mnemonic',
    '助記詞生成失敗': 'Mnemonic generation failed',
    '動態與獎勵': 'Dynamics & rewards',
    '匯出 CSV': 'Export CSV',
    '即見節點品牌控制台。': 'and see the node console.',
    '原始碼永久固化（GitHub）': 'Source code permanently frozen (GitHub)',
    '參考價未配置': 'Reference price not configured',
    '台 · 總時權': 'units · total uptime',
    '台（與 /network 同源）': 'units (same source as /network)',
    '合約': 'Contract',
    '合計：': 'Total:',
    '同時輪值）。': 'rotating together).',
    '商品 ID：': 'Item ID:',
    '啟動節點（把': 'Start node (replace',
    '在伺服器上執行下方指令，節點金鑰代簽轉出': 'Run the command on the server; node key signs the transfer',
    '在想些什麼？': 'What are you thinking about?',
    '在線挖礦中 · 已上鏈': 'Mining online · on-chain',
    '在線礦工排行（': 'Miner ranking (',
    '地址已複製': 'Address copied',
    '基礎': 'Base',
    '基礎時權': 'Base uptime',
    '塊／每期': 'blocks / per round',
    '多節點注意事項': 'Multi-node notes',
    '失敗：': 'Failed:',
    '安全審計': 'Security audit',
    '官方預編譯的': 'officially pre-built',
    '密碼錯誤': 'Wrong password',
    '審計': 'Audit',
    '審計暫不可用': 'Audit temporarily unavailable',
    '審計項目': 'Audit items',
    '導入助記詞': 'Import mnemonic',
    '導入失敗，請稍後再試': 'Import failed, try again later',
    '導入成功：': 'Import success:',
    '尚未提供流動性': 'No liquidity provided yet',
    '尚未收到': 'Not received yet',
    '尚未綁定': 'Not bound yet',
    '尚無 CDP 倉位': 'No CDP positions',
    '尚無 NFT 集合': 'No NFT collections',
    '尚無代幣轉移': 'No token transfers',
    '尚無存款': 'No deposits',
    '尚無成交數據': 'No trade data',
    '尚無手續費累積': 'No fees accumulated',
    '尚無機器人': 'No bots',
    '尚無留言': 'No comments',
    '尚無訂單數據': 'No order data',
    '尚無評價': 'No reviews',
    '尚無資產': 'No assets',
    '尚無農場': 'No farms',
    '已停止': 'Stopped',
    '已儲存': 'Saved',
    '已全量授權。tx:': 'Fully approved. tx:',
    '已刪除': 'Deleted',
    '已審計': 'Audited',
    '已平倉': 'Closed',
    '已授權。tx:': 'Approved. tx:',
    '已提交上鏈：': 'Submitted on-chain:',
    '已撤銷。tx:': 'Revoked. tx:',
    '已更新': 'Updated',
    '已注入，份額': 'Injected, share',
    '已清算': 'Liquidated',
    '已發行。合約：': 'Issued. Contract:',
    '已移除 · 贖回': 'Removed · redeemed',
    '已結束': 'Ended',
    '已結清並釋放抵押品': 'Settled and collateral released',
    '已結算': 'Settled',
    '已複製': 'Copied',
    '已複製 WIF 私鑰，請妥善保管': 'WIF private key copied — keep it safe',
    '已複製貼文連結': 'Post link copied',
    '已複製連結': 'Link copied',
    '已解鎖（密碼鎖已清除）': 'Unlocked (password lock cleared)',
    '已設置': 'Set',
    '已設置，請牢記管理密碼': 'Set — remember the admin password',
    '已跨鏈': 'Cross-chain done',
    '已轉入交易所': 'Moved to exchange',
    '已鑄': 'Minted',
    '已隱藏：': 'Hidden:',
    '已領取': 'Claimed',
    '平倉': 'Close',
    '平倉成功，抵押品已釋放': 'Closed — collateral released',
    '廣告已發佈（ID #': 'Ad published (ID #',
    '心跳': 'Heartbeat',
    '心跳上鏈（近 200 塊）': 'Heartbeat on-chain (last ~200 blocks)',
    '快捷入金至交易所': 'Quick deposit to exchange',
    '快照快速同步（重要）': 'Snapshot fast-sync (important)',
    '思考中…': 'Thinking…',
    '恢復失敗，請稍後再試': 'Recovery failed, try again later',
    '恢復成功：': 'Recovery success:',
    '成為節點後：每塊': 'After becoming a node: per block',
    '我已了解並接受無常損失風險': 'I understand and accept impermanent loss risk',
    '我的 CDP 倉位': 'My CDP positions',
    '我的身份': 'My identity',
    '我要開播': 'Start live',
    '抵押 TACm': 'Stake TACm',
    '按讚失敗：': 'Like failed:',
    '挖礦獎勵': 'Mining reward',
    '掛單 · 成交紀錄 · 撤單': 'Orders · Trades · Cancel',
    '推完後將': 'After posting, will',
    '提案與投票': 'Proposals & voting',
    '提現成功（鏈上手續費': 'Withdraw success (on-chain fee',
    '提領到綁定錢包': 'Withdraw to bound wallet',
    '提領失敗': 'Withdraw failed',
    '提領節點獎勵': 'Claim node rewards',
    '換成自己的公網 IP）：': 'with your public IP):',
    '收款': 'Receive',
    '數位收藏': 'Digital collectibles',
    '數量須大於 0': 'Amount must be > 0',
    '方式': 'Method',
    '方式一': 'Method 1',
    '方式一：下載節點程式包（免編譯，推薦）': 'Method 1: download node package (no compile, recommended)',
    '方式二：一鍵自動安裝（需 Go 編譯）': 'Method 2: one-click auto-install (needs Go)',
    '暫無市場': 'No markets',
    '暫無市場數據': 'No market data',
    '暫無池': 'No pools',
    '更新失敗——顯示上次數據': 'Update failed — showing last data',
    '更新於': 'Updated at',
    '最近入帳區塊': 'Latest deposit block',
    '未命名': 'Untitled',
    '未設置': 'Not set',
    '本地大模型（Qwen · llama.cpp）': 'Local LLM (Qwen · llama.cpp)',
    '本節點': 'This node',
    '檔案超過 64MB 上限': 'File exceeds 64MB limit',
    '正在探測全網節點…': 'Probing network nodes…',
    '正在直播': 'Live now',
    '此訪客地址無可備份私鑰': 'This guest address has no backup key',
    '每地址僅一次、限量 50 名': 'once per address, limit 50',
    '永久運行（systemd，開機自啟）：': 'Run forever (systemd, auto-start):',
    '流動性與兌換': 'Liquidity & swaps',
    '深度圖': 'Depth chart',
    '添加成功 · 獲得 LP': 'Added · got LP',
    '清算': 'Liquidate',
    '清算價 $': 'liquidation price $',
    '清算完成 · 取得': 'Liquidated · got',
    '清除密碼鎖': 'Clear password lock',
    '滑點：自動': 'Slippage: auto',
    '無交易紀錄': 'No transactions',
    '無常損失風險（Impermanent Loss）': 'Impermanent Loss risk',
    '無法驗證': 'Cannot verify',
    '獲批者:': 'Approver:',
    '現貨交易': 'Spot Trading',
    '留言失敗：': 'Comment failed:',
    '異常:': 'Error:',
    '異常清單：': 'Error list:',
    '登入失敗': 'Login failed',
    '登入成功，重新載入…': 'Login success, reloading…',
    '發佈動態 / 商品': 'Post status / item',
    '發現異常': 'Anomaly detected',
    '發送失敗：': 'Send failed:',
    '目前沒有直播，快來開播第一場！': 'No live streams yet — start the first one!',
    '直播': 'Live',
    '確定刪除這則貼文？': 'Delete this post?',
    '確定刪除這支影片？': 'Delete this video?',
    '確定取消訂單？': 'Cancel this order?',
    '確認全額償還並平倉？抵押品將全部釋放。': 'Repay in full and close? All collateral will be released.',
    '確認失敗': 'Confirm failed',
    '確認支付廣告預算（TACM 全額入資金池）並開始投放？': 'Confirm the ad budget (all TACM into the pool) and start?',
    '確認送出': 'Confirm',
    '秒級就緒': 'ready in seconds',
    '移除 LP 數量（最多': 'Remove LP amount (max',
    '種子': 'Seeds',
    '第 1 步：確保節點錢包有幣': 'Step 1: fund the node wallet',
    '第 2 步：發質押交易': 'Step 2: send the stake tx',
    '第 3 步：領取獎勵': 'Step 3: claim rewards',
    '第三方機構級審計（CertiK / SlowMist）': 'Third-party institutional audit (CertiK / SlowMist)',
    '筆）': 'entries)',
    '策略自動買賣': 'Automated strategy trading',
    '管理密碼': 'Admin password',
    '節點 45% · 獎勵池 15% · 礦工 40%': 'Node 45% · Pool 15% · Miners 40%',
    '節點啟動時加': 'add at node start',
    '節點架設者面板': 'Node Operator Panel',
    '節點步驟': 'Node steps',
    '節點獎勵': 'Node rewards',
    '節點獎勵提取（僅本機 · M77）': 'Node reward withdrawal (local only · M77)',
    '節點獎勵餘額（鏈上）': 'Node reward balance (on-chain)',
    '節點獎勵驗證器（鏈上）': 'Node reward verifier (on-chain)',
    '節點監控（全網統一視界）': 'Node monitor (chain-wide view)',
    '節點程式包。': 'node package.',
    '節點要求': 'Node requirements',
    '節點面板': 'Node Panel',
    '純節點程式': 'Node-only program',
    '累計入帳': 'Total received',
    '累計銷毀': 'Total burned',
    '累計鑄造': 'Total minted',
    '結束失敗：': 'End failed:',
    '結束直播': 'End live',
    '結算週期：每': 'Settlement: every',
    '綁定失敗': 'Bind failed',
    '綁定成功：': 'Bind success:',
    '綁定收款地址': 'Bind receive address',
    '網絡概況': 'Network overview',
    '網路錯誤:': 'Network error:',
    '編輯': 'Edit',
    '編輯失敗': 'Edit failed',
    '編輯影片描述：': 'Edit video description:',
    '編輯貼文內容：': 'Edit post content:',
    '總估值（USDT）': 'Total value (USDT)',
    '總時權': 'Total uptime',
    '自動質押、直接成為節點——資金來自創世社群基金，': 'auto-staked to become a node — funded by the genesis community fund,',
    '自動配置）。': 'auto-config).',
    '自訂…': 'Custom…',
    '行情載入失敗': 'Market data load failed',
    '複製助記詞': 'Copy mnemonic',
    '複製失敗': 'Copy failed',
    '規則式助手': 'Rule-based assistant',
    '訂單 #': 'Order #',
    '設定密碼鎖': 'Set password lock',
    '設定密碼鎖（防止旁人查看私鑰）：': 'Set password lock (hide private key from others):',
    '設置失敗': 'Set failed',
    '設置管理密碼': 'Set admin password',
    '註冊失敗': 'Register failed',
    '註冊成功，重新載入…': 'Register success, reloading…',
    '評價列表': 'Reviews',
    '評價失敗：': 'Review failed:',
    '說點什麼…': 'Say something…',
    '請先勾選「我已了解並接受無常損失風險」': 'Check "I understand and accept impermanent loss risk" first',
    '請先設置管理密碼（上方設置區）': 'Set the admin password first (section above)',
    '請先貼上 24 詞助記詞': 'Paste the 24-word mnemonic first',
    '請先貼上 WIF 私鑰': 'Paste the WIF private key first',
    '請先輸入地址': 'Enter an address first',
    '請先選中農場': 'Select a farm first',
    '請填寫份額': 'Enter the share',
    '請填寫帳號、基準價與數量': 'Enter account, base price and qty',
    '請填寫廣告標題與預算': 'Enter ad title and budget',
    '請填寫數量': 'Enter an amount',
    '請手動複製上方助記詞': 'Copy the mnemonic above manually',
    '請立即備份：': 'Back it up now:',
    '請輸入 From 與 To': 'Enter From and To',
    '請輸入 Owner 與操作員': 'Enter Owner and operator',
    '請輸入 Token ID': 'Enter Token ID',
    '請輸入償還數量': 'Enter the repay amount',
    '請輸入名稱與代號': 'Enter name and symbol',
    '請輸入抵押 TACm 與借出 TiUSD 數量': 'Enter TACm to stake and TiUSD to borrow',
    '請輸入授權地址與 Token ID': 'Enter approval address and Token ID',
    '請輸入接收地址': 'Enter the receive address',
    '請輸入操作員地址': 'Enter the operator address',
    '請輸入數字 Token ID': 'Enter a numeric Token ID',
    '請輸入申訴原因': 'Enter the appeal reason',
    '請輸入直播標題': 'Enter the live title',
    '請輸入管理密碼': 'Enter the admin password',
    '請輸入總供應量': 'Enter the total supply',
    '請輸入追加數量': 'Enter the top-up amount',
    '請輸入還款金額': 'Enter the repayment amount',
    '請輸入錢包地址': 'Enter a wallet address',
    '貼文連結:': 'Post link:',
    '資產總覽': 'Asset overview',
    '資產聚合載入失敗': 'Asset aggregation load failed',
    '賣家地址：': 'Seller address:',
    '賣家：': 'Seller:',
    '質押成為節點（去中心化共識）': 'Stake to become a node (decentralized consensus)',
    '賬本異常': 'Ledger anomaly',
    '購買商品（TACM 鏈上轉帳）': 'Buy item (TACM on-chain transfer)',
    '購買意向失敗：': 'Purchase intent failed:',
    '跨鏈橋多簽＋SPV 頭鏈驗證': 'Bridge multi-sig + SPV header verification',
    '跨鏈橋未啟用': 'Bridge not enabled',
    '身份（錢包地址）': 'Identity (wallet address)',
    '載入中…': 'Loading…',
    '載入失敗：': 'Load failed:',
    '輪）': 'rounds)',
    '轉出': 'Transfer out',
    '轉帳': 'Transfer',
    '追加': 'Top up',
    '送出中…': 'Submitting…',
    '連線失敗': 'Connection failed',
    '進行中': 'In progress',
    '運行你的節點': 'Run your node',
    '還款成功 ·': 'Repaid ·',
    '金額：': 'Amount:',
    '錢包已鎖定': 'Wallet locked',
    '錢包已鎖定，請輸入密碼：': 'Wallet locked — enter password:',
    '鎖定資產': 'Lock assets',
    '鏈上一致': 'On-chain consistent',
    '鏈上可驗證': 'On-chain verifiable',
    '鏈上問答': 'On-chain Q&A',
    '鏈上在線礦工（': 'Miners online (',
    '鏈上審計（區塊 · 交易 · 合約）': 'On-chain audit (blocks · txs · contracts)',
    '鏈上搜尋': 'On-chain search',
    '鏈上獎勵不一致，需立即檢查': 'On-chain rewards inconsistent — check immediately',
    '鏈上費（2%）：': 'On-chain fee (2%):',
    '鏈上錢包': 'On-chain wallet',
    '鏈上驗證：一致': 'On-chain verification: consistent',
    '鏈節點': 'Chain node',
    '門檻：質押 ≥ 1,000 TACm': 'Threshold: stake ≥ 1,000 TACm',
    '閃兌與訂單': 'Swap & orders',
    '開倉 Mint TiUSD': 'Open position · Mint TiUSD',
    '開倉成功 #': 'Position opened #',
    '開啟中': 'Opening',
    '開播': 'Go live',
    '開播失敗：': 'Go live failed:',
    '隱藏': 'Hide',
    '離線 · 已上鏈': 'Offline · on-chain',
    '離開': 'Leave',
    '預估手續費：': 'Estimated fee:',
    '預估手續費：—': 'Estimated fee: —',
    '領取上鏈中…': 'Claiming on-chain…',
    '顯示在動態流第 4 則之後、影片流每 5 支之後插播': 'Shown after the 4th feed post and every 5 videos',
    '驗證中…': 'Verifying…',
    '驗證失敗': 'Verification failed',
    '驗證與挖礦': 'Verify & mine',
    '（不是有效高度、交易哈希或地址）': '(not a valid height, tx hash or address)',
    '（其他資產：': '(other assets:',
    '（最後 #': '(last #',
    '（未鑄造）': '(not minted)',
    '（訪客）': '(guest)',
    '（費用': '(fee',
    '（額滿即止）。領取方式：': '(while supplies last). How to claim:',
    '）。預算': '). Budget',
    '），節點地址＝你的收款錢包，出塊獎勵自動入帳。': '), node address = your receive wallet; block rewards auto-credited.',
    '，不再從創世逐塊重放（省 10 分鐘以上）。': ', no more genesis replay from block 0 (saves 10+ min).',
    '，備註：': ', note:',
    '，否則回到創世重放、長時間落後。': ', otherwise it replays from genesis and falls far behind.',
    '，建議節點達': ', recommend at least',
    '，指定模式加': ', add --mode for',
    '：新節點首次上線可領取': ': new nodes can claim once at first start',
    '：首次啟動自動生成節點金鑰（': ': auto-generates the node key on first start (',
    '入帳筆數': 'payments',
    '頁碼': 'Page',
    '每頁': 'per page',
    '相關交易': 'related transactions',
    '上一頁': 'Prev',
    '區塊交易': 'Block Transactions',
    '已最終化': 'Finalized',
    '共識': 'Consensus',
    '難度': 'Difficulty',
    '記憶體池': 'Mempool',
    '運行時間': 'Uptime',
    '挹注比例': 'Top-up ratio',
    '總抵押': 'Total collateral',
    '平均抵押率': 'Avg collateral ratio',
    '抵押率': 'Collateral ratio',
    '每塊:': 'Per block:',
    '已質押:': 'Staked:',
    '我的份額:': 'My share:',
    '曝光': 'Impressions',
    '點擊': 'Clicks',
    '追蹤中': 'Following',
    '新增朋友': 'Add friend',
    '影片': 'Video',
    '市集': 'Market',
    '動態': 'Post',
    '手續費': 'Fee',
    '節點': 'Node',
    '總預算': 'Total budget',
    '每日': 'Daily',
    '動態流+影片流': 'Feed + Video streams',
    '確認付款（一鍵轉帳）': 'Confirm payment (one-click transfer)',
    '市集請填價格': 'Market: enter a price',
    '請輸入金額': 'Enter an amount',
    '請填寫商品名稱與價格': 'Enter item name and price',
    '請填寫廣告標題與總預算': 'Enter ad title and total budget',
    '同步區塊': 'Synced block',
    '付款成功　實付': 'Payment success · paid',
    '載入失敗': 'Load failed',
    '失敗': 'Failed',
    '（不含錢包/交易所/社群等 DApp）。下載即用，不需 Go、不需編譯、不需整套系統原始碼。': '(no wallet/exchange/community DApps). Download and run — no Go, no compile, no full source needed.',
    '一行一鍵安裝（自動下載／編譯／啟動）。testnet 使用獨立鏈 ID': 'One-line one-click install (auto download/compile/start). testnet uses a separate chain ID',
    '（自動裝 Go／下載／編譯／systemd 或 nohup 啟動／偵測公網 IP）。Linux 伺服器用 systemd（開機自啟），手機 Termux 自動用 nohup；自訂埠加': '(auto-installs Go / downloads / compiles / starts via systemd or nohup / detects public IP). Linux servers use systemd (auto-start on boot); Termux auto-uses nohup; add --port for a custom port',
    '。⚠ 此腳本為早期版本（需 Go 編譯），主網現行請優先使用上方': '. ⚠ This script is an early version (needs Go); on mainnet prefer the',
    '開放 TCP 8080 埠（P2P + RPC 同埠合併）；8081 埠供節點品牌控制台': 'Open TCP 8080 (P2P + RPC merged); 8081 serves the node console',
    '返回交易雜湊即領取成功；入塊後 1,000 TACm 入錢包並自動鎖入質押池。': 'A tx hash is returned on success; once mined, 1,000 TACm enters the wallet and is auto-locked into the staking pool.',
    ',與主流 PoS（以太坊等）一致：節點把 TACm 鎖進鏈上質押池 → 進入節點集（每 60 秒由鏈上質押重放重建，全網一致）→ 輪值出塊、由在線節點均分': ', like mainstream PoS (Ethereum etc.): the node locks TACm into the on-chain staking pool → enters the node set (rebuilt chain-wide every 60s from on-chain stakes) → takes turns producing blocks, split among online nodes',
    '（質押高者優先入集）。質押幣鎖在鏈上質押池（鏈上可查），不可挪用。': '(higher stakes enter first). Staked coins stay locked in the on-chain pool (auditable), cannot be diverted.',
    '新節點初始餘額為 0，需先由其他持有者轉入（例如從創世節點錢包或交易所轉到你的節點地址）。節點地址＝節點金鑰對應地址，見節點面板／': 'New nodes start with 0 balance; someone must first transfer in (e.g. from the genesis wallet or exchange to your node address). Node address = address of the node key; see Node Panel /',
    '在節點控制台（節點面板）點「質押成為節點」輸入金額，或直接呼叫 API：': 'In the node console (Node Panel) click "Stake to become a node", enter an amount, or call the API directly:',
    '成功後返回交易雜湊；交易入塊後最多 1 分鐘，節點自動進入節點集並開始輪值出塊。': 'A tx hash is returned on success; within 1 minute of mining the node auto-joins the node set and starts producing blocks.',
    '由鏈上在線節點均分、直接入你的節點錢包（coinbase:node）。另可開啟算力成為礦工，按在線算力瓜分': 'split among online nodes straight into your node wallet (coinbase:node). You can also mine with hashrate and share',
    '礦工獎勵（coinbase:miner）。兩者可同時開啟。': 'miner rewards (coinbase:miner). Both can run at once.',
    '，會直接下載創世節點的最新狀態快照（創世+Tip+全帳戶餘額），': ', it downloads the genesis node latest state snapshot (genesis + Tip + all balances),',
    '無磁碟的免費雲節點（Render 等）每次重啟都必須帶此參數': 'diskless free cloud nodes (Render etc.) must pass this flag on every restart',
    '（容忍 1 個離線）。加入的節點越多，鏈的可用性與去中心化程度越高。': '(tolerates 1 offline). More nodes = higher availability and decentralization.',
    '節點與整套鏈的原始碼固化到 GitHub，任何人可 clone、審計、部署（一次一條）：': 'The node and full chain source is frozen on GitHub — anyone can clone, audit, deploy (one at a time):',
    '貼回本頁 seed 列表（或告知我），即可成為全網永久可查證的開源創世節點。': 'Paste it back into this page seed list (or tell me) to become a permanently verifiable open-source genesis node.',
    '管理你的節點：綁定收款地址、查詢節點獎勵（coinbase:node 45%，在線均分）、將獎勵提領轉入 TACm web3 錢包。操作需節點管理密碼。': 'Manage your node: bind a receive address, check node rewards (coinbase:node 45%, split online), withdraw rewards to your TACm web3 wallet. Requires the node admin password.',
    '首次使用需設置管理密碼（至少 6 字元），保護綁定與提領操作。': 'First use: set an admin password (min 6 chars) to protect binding and withdrawals.',
    '填寫你的 TACm web3 錢包地址（tx01…），節點獎勵提領將轉入此地址。': 'Enter your TACm web3 wallet address (tx01…); node reward withdrawals go there.',
    '掃描最近 200 個區塊的 coinbase:node 入帳與心跳——即使單筆獎勵很小，也能確認節點是否真的在領獎。': 'Scans the last 200 blocks for coinbase:node payments and heartbeats — even tiny rewards confirm the node is really earning.',
    '節點身份密鑰（助記詞/私鑰）儲存在節點資料目錄 node_config.json——請立即備份，遺失即無法找回節點身份與獎勵。': 'The node identity key (mnemonic/private key) is stored at node_config.json in the data dir — back it up now; losing it means losing the node identity and rewards.',
    '以節點私鑰簽名，把節點獎勵轉入綁定錢包（上鏈、可於區塊瀏覽器查詢）。': 'Signs with the node private key and transfers node rewards to the bound wallet (on-chain, viewable in the explorer).',
    '提供流動性後，若代幣相對價格大幅波動，退出時資產價值可能低於單純持有兩種代幣。高波動交易對風險更大；手續費與挖礦獎勵可部分彌補。': 'After providing liquidity, if relative token prices swing sharply, the exit value may be lower than simply holding both tokens. High-volatility pairs are riskier; fees and mining rewards partially compensate.',
    '）：IL ≈ 2√r/(1+r) − 1。r=2 時約 −5.7%；r=4 時約 −20%。': ') IL ≈ 2√r/(1+r) − 1. At r=2 ≈ −5.7%; at r=4 ≈ −20%.',
    '核心鏈一致性（PoS/PoT coinbase 全網複驗）': 'Core chain consistency (PoS/PoT coinbase re-verified chain-wide)',
    '方法可鏈上複驗：瀏覽器交易明細、ledger audit API、跨鏈橋事件日誌皆公開。': 'Verifiable on-chain: browser tx details, ledger audit API, bridge event logs are all public.',
    '開倉（抵押率 ≥ 150% · 清算線 130% · 穩定費年息 2% · 清算罰金 5%）': 'Open (collateral ratio ≥ 150% · liquidation line 130% · stability fee 2% APR · liquidation penalty 5%)',
    '助記詞為 24 個英文單詞，等同私鑰完整備份；請抄寫並妥善保管，勿上傳或截圖外傳': 'The mnemonic is 24 English words, a full backup of the private key; write it down and keep it safe — never upload or screenshot it',
    '每塊獎勵分潤：節點 45%（在線節點均分）／獎勵池 15%／礦工 40%（按在線算力瓜分）': 'Per-block reward split: node 45% (split among online) / pool 15% / miners 40% (by online hashrate)',
    '自動偵測環境（systemd / nohup / render）、自動裝 Go、下載、編譯、啟動、偵測公網 IP。Render：推 GitHub → 新建 Web Service → Go runtime（': 'Auto-detects environment (systemd / nohup / render), installs Go, downloads, compiles, starts, detects public IP. Render: push GitHub → new Web Service → Go runtime (',
    '一次一條執行；推完後節點原始碼永久公開可查證、可 clone 部署。': 'Run one at a time; after pushing, the node source stays public, verifiable and clonable.',
    '外部網路無法呼叫（僅 127.0.0.1），節點金鑰資產安全。': 'Not callable from external networks (127.0.0.1 only) — node key assets stay safe.',
    '參考價未配置（設定 TAC_TACM_PRICE_USD 後啟用）': 'Reference price not configured (enable via TAC_TACM_PRICE_USD)',
    '（安裝本地模型請執行 scripts/install-llama.sh）': '(to install the local model run scripts/install-llama.sh)',
    '約 獲得': '≈ get',
    '借': 'borrow',
    '債務': 'debt',
    '可清': 'liquidatable',
    '開倉': 'Open',
};
  function reverseMap(m) {
    var r = {};
    var keys = Object.keys(m);
    for (var i = 0; i < keys.length; i++) { r[m[keys[i]]] = keys[i]; }
    return r;
  }
  // M100：社群貼文/影片 編輯刪除＋個人資料（對外名稱/頭像）＋影片載入/音量
  zh2en['編輯'] = 'Edit'; zh2en['刪除'] = 'Delete'; zh2en['儲存'] = 'Save';
  zh2en['載入中…'] = 'Loading…'; zh2en['未命名'] = 'Unnamed';
  zh2en['未偵測到 MetaMask，請先安裝後重試'] = 'No MetaMask detected. Please install it first'; zh2en['連線失敗'] = 'Connection failed';
  zh2en['對外顯示名稱'] = 'Display Name'; zh2en['個人頭像'] = 'Avatar';
  zh2en['已儲存'] = 'Saved'; zh2en['已更新'] = 'Updated'; zh2en['已刪除'] = 'Deleted';
  zh2en['編輯個人資料'] = 'Edit Profile'; zh2en['編輯貼文內容：'] = 'Edit post content:';
  zh2en['編輯影片描述：'] = 'Edit video description:';
  zh2en['確定刪除這則貼文？'] = 'Delete this post?'; zh2en['確定刪除這支影片？'] = 'Delete this video?';
  zh2en['儲存失敗'] = 'Save failed'; zh2en['編輯失敗'] = 'Edit failed'; zh2en['刪除失敗'] = 'Delete failed';
  zh2en['互動積分'] = 'Interaction Points'; zh2en['輸入顯示名稱…'] = 'Enter display name...'; zh2en['https://… 或 /static/…'] = 'https://... or /static/...';
  // M101：錢包資產清單 + 交易所 K 線/深度圖 中英映射。
  var m101 = {
    '資產清單': 'Asset List', '總估值（USDT）': 'Total Value (USDT)', '預估手續費：': 'Est. fee:',
    '隱藏': 'Hide', '已隱藏：': 'Hidden:', '尚無資產': 'No assets yet', '尚無成交數據': 'No trades yet',
    '尚無訂單數據': 'No orders yet', 'K 線': 'K-Line', '深度圖': 'Depth', '點差': 'Spread',
    '訂單簿': 'Order Book', '最近成交': 'Recent Trades', '交易市場': 'Trading Market',
    '下單': 'Place Order', '買入': 'Buy', '賣出': 'Sell', '限價': 'Limit', '市價': 'Market',
    '查詢': 'Query', '兌換': 'Swap', '啟動機器人': 'Start Bot', '撤單': 'Cancel',
    '載入失敗': 'Load failed', '載入中…': 'Loading…'
  };
  Object.keys(m101).forEach(function (k) { if (!(k in zh2en)) { zh2en[k] = m101[k]; } });
  // M150g：i18n 缺口修正——①data-i18n 指向不存在的 key（profile/tx/videos）補 zh+en；
  // ②硬編碼中文模板（auth/block/c2c/dashboard/defi/node_panel/join）主要 UI 短語補 zh2en 雙向字典（英文模式 TreeWalker 自動替換）。
  dict.zh['tiusd.title'] = 'TiUSD 穩定幣監控台'; dict.zh['tiusd.sub'] = '儲備量 · 鑄造 · 流通 · 流向 · 跨鏈明細 —— 確保總供應不超額膨脹';
  dict.zh['tiusd.overview'] = '總覽'; dict.zh['tiusd.supply'] = '流通供應 (TiUSD)'; dict.zh['tiusd.cap'] = '市值上限 (1:1 市值)'; dict.zh['tiusd.mcap'] = '市值規則'; dict.zh['tiusd.listed'] = '已上市 · 市價 $'; dict.zh['tiusd.unlisted'] = '未上市 · 按 1:0.330 計算';
  dict.zh['tiusd.floor'] = '供應下限 (33%)'; dict.zh['tiusd.remaining'] = '剩餘可鑄'; dict.zh['tiusd.util'] = '供應利用率';
  dict.zh['tiusd.peg'] = '錨定價格'; dict.zh['tiusd.health'] = '膨脹檢查'; dict.zh['tiusd.h.unlisted'] = '未上市 · 0.330 基準'; dict.zh['tiusd.export'] = '匯出帳本 CSV'; dict.zh['tiusd.chart'] = '供應 vs 市值上限趨勢'; dict.zh['tiusd.chartNote'] = '累計鑄造（ledger mint 累加）對照市值上限——確認無超額膨脹。'; dict.zh['tiusd.chartWait'] = '暫無鑄造歷史'; dict.zh['tiusd.pie'] = '供應來源占比'; dict.zh['tiusd.positions'] = '借款頭寸（全鏈 DeFi）'; dict.zh['tiusd.addr2'] = '地址'; dict.zh['tiusd.collateral'] = '抵押'; dict.zh['tiusd.borrow2'] = '借出'; dict.zh['tiusd.status2'] = '狀態'; dict.zh['tiusd.noPos'] = '尚無借款頭寸'; dict.zh['tiusd.posTotals'] = '全鏈總借款：'; dict.zh['tiusd.posColl'] = '總抵押價值：'; dict.zh['tiusd.posActive'] = '活躍：'; dict.zh['tiusd.bridgeNet'] = '跨鏈淨流入 (轉入−轉出)：'; dict.zh['tiusd.bridgeDone'] = '已完成'; dict.zh['tiusd.alerts'] = '近期警報事件（30 天）'; dict.zh['tiusd.alertsLevel'] = '等級'; dict.zh['tiusd.alertsType'] = '類型'; dict.zh['tiusd.alertsDetail'] = '明細'; dict.zh['tiusd.alertsTime'] = '時間';  dict.zh['tiusd.alertsCfg'] = '⚙ 配置';
 dict.zh['tiusd.alertsCfgTitle'] = '警報閾值配置';
  dict.zh['tiusd.alertsNear'] = 'WARN 閾值（0-1）：';
  dict.zh['tiusd.alertsOver'] = 'CRIT 閾值（0-1）：';
  dict.zh['tiusd.alertsWebhook'] = 'Webhook 通知 URL：';
  dict.zh['tiusd.alertsWebhookNote'] = '警報產生時 POST JSON（{event:\'tiusd_alert\', alerts:[…]}）到該 URL；留空即關閉。';
  dict.zh['tiusd.alertsSaved'] = '已保存';
  dict.zh['tiusd.alertsFail'] = '保存失敗';
  dict.zh['tiusd.cancel'] = '取消';
  dict.zh['tiusd.save'] = '保存';
 dict.zh['tiusd.bridgeStatus'] = '跨鏈橋狀態';
  dict.zh['tiusd.bridgePending'] = '待處理';
  dict.zh['tiusd.bridgeChains'] = '支援鏈數';
  dict.zh['tiusd.bridgeFee'] = '手續費率';
  dict.zh['tiusd.bridgePaused'] = '暫停中';
 dict.zh['tiusd.poolOps'] = '資金池近期操作（TiUSD）';
  dict.zh['tiusd.poolAcct'] = '池帳戶';
  dict.zh['tiusd.memo'] = '備註';
  dict.zh['tiusd.yes'] = '是';
  dict.zh['tiusd.no'] = '否';
  dict.zh['tiusd.time'] = '時間';
dict.zh['tiusd.alertsNone'] = '尚無警報事件'; dict.zh['tiusd.reserves'] = '真實儲備';
  dict.zh['tiusd.stablePool'] = '兌換儲備池 (stable-pool)'; dict.zh['tiusd.feePool'] = '手續費池 (fee)';
  dict.zh['tiusd.reserveNote'] = 'TiUSD 有真實儲備背書（USDT/TACm）才可兌換；無儲備時僅能 TACm 閃兌。';
  dict.zh['tiusd.mint'] = '鑄造與銷毀'; dict.zh['tiusd.totalMinted'] = '累計鑄造'; dict.zh['tiusd.totalBurned'] = '累計銷毀';
  dict.zh['tiusd.net'] = '淨鑄造'; dict.zh['tiusd.updated'] = '更新時間';
  dict.zh['tiusd.mintNote'] = '產生途徑：CDP 抵押借貸、native-swap TACm 閃兌、stable-swap（受儲備限制）、供給層補鑄；還款即銷毀。';
  dict.zh['tiusd.flow'] = 'TiUSD 流向統計（帳本分類）'; dict.zh['tiusd.category'] = '來源 / 流向'; dict.zh['tiusd.inflow'] = '流入 (TiUSD)';
  dict.zh['tiusd.outflow'] = '流出 (TiUSD)'; dict.zh['tiusd.net2'] = '淨額';
  dict.zh['tiusd.bridgeTitle'] = '跨鏈轉出 / 轉入明細（TiUSD）'; dict.zh['tiusd.dir'] = '方向'; dict.zh['tiusd.route'] = '鏈路由';
  dict.zh['tiusd.amount'] = '金額'; dict.zh['tiusd.status'] = '狀態'; dict.zh['tiusd.addr'] = '地址'; dict.zh['tiusd.txhash'] = '交易哈希';
  dict.zh['tiusd.recent'] = '近期 TiUSD 流水（帳本）'; dict.zh['tiusd.time'] = '時間'; dict.zh['tiusd.account'] = '帳戶'; dict.zh['tiusd.memo'] = 'Memo';
  dict.zh['tiusd.na'] = '未啟用 (未設 TACM_MAX_SUPPLY)'; dict.zh['tiusd.h.ok'] = '健康 · 未超額'; dict.zh['tiusd.h.near'] = '接近上限 (>95%)';
  dict.zh['tiusd.h.over'] = '已超上限！'; dict.zh['tiusd.h.dis'] = '未設上限 (未啟用)';
  dict.zh['tiusd.f.cdp'] = 'CDP 抵押借貸鑄造'; dict.zh['tiusd.f.repay'] = '還款 / 銷毀'; dict.zh['tiusd.f.stable'] = '穩定幣兌換 stable-swap';
  dict.zh['tiusd.f.native'] = 'TACm 閃兌 native-swap'; dict.zh['tiusd.f.seed'] = '池創世注資 (鏈上鑄造)'; dict.zh['tiusd.f.floor'] = '供給層補鑄 (下限)';
  dict.zh['tiusd.f.bridge'] = '跨鏈橋'; dict.zh['tiusd.f.transfer'] = '一般轉帳'; dict.zh['tiusd.f.other'] = '其他 (未分類)'; dict.zh['assets.f.other'] = '其他 (未分類)';
  dict.zh['tiusd.din'] = '轉入'; dict.zh['tiusd.dout'] = '轉出';
  dict.zh['tiusd.noBridge'] = '尚無 TiUSD 跨鏈記錄'; dict.zh['tiusd.noLedger'] = '尚無 TiUSD 流水';
  dict.zh['assets.title'] = '資產中心'; dict.zh['assets.sub'] = '鏈上資產總覽：供應 · 儲備 · 流向 · 跨鏈明細 —— 確保無超額膨脹';
  dict.zh['assets.tabTacm'] = 'TACm'; dict.zh['assets.export'] = '匯出帳本 CSV';
  dict.zh['assets.tools'] = '開發者工具';
  dict.zh['assets.signTool'] = '簽名工具台 (EIP-191/712)';
  dict.zh['assets.tokenStudio'] = '代幣工作室 (Token Studio)';
  dict.zh['assets.contractVerify'] = '合約驗證中心 (Contract Verifier)';
  dict.zh['assets.blockExplorer'] = '區塊瀏覽器';
  dict.zh['assets.rpcDocs'] = 'API 文檔';
dict.zh['assets.secTitle'] = '錢包安全與多簽';
dict.zh['assets.secNote'] = '已支援 EIP-191 / EIP-712 標準簽名與 MetaMask 級多幣種管理；多簽（Multisig）採用閾值簽名方案，交易需指定數量簽署人共同授權。';
dict.zh['assets.secList'] = '• 簽名工具：訊息 / TypedData 簽名與驗證<br>• 多簽設定：可在 Web3 錢包建立 2/3、3/5 等多簽帳戶<br>• 硬體錢包：支援 Ledger 風格離線簽名（簽名僅本地產生，私鑰永不離機）';
  dict.zh['assets.tacmPrice'] = '市場價';
  dict.zh['assets.tacmUpdated'] = '更新時間';
  dict.zh['assets.tacmPoolOps'] = '資金池近期操作（TACm）';
  dict.zh['assets.poolAcct2'] = '池帳戶';
  dict.zh['assets.tacmTrend'] = '已出塊產出 vs 供應上限趨勢';
  dict.zh['assets.tacmTrendNote'] = '累計產出（ledger mint/reward 累加）對照供應上限——確認開採進度。';
  dict.zh['assets.tacmPie'] = '開採來源占比';
  dict.zh['assets.tacmAlerts'] = '開採進度警報';
  dict.zh['assets.tacmBridge'] = '跨鏈橋狀態';
  dict.zh['assets.tacmAlertsDisabled'] = '未設上限（減半模式）——警報停用';
  dict.zh['assets.tacmAlertsSummary'] = '開採進度：';
  dict.zh['assets.tacmAlertsNearLb'] = 'WARN 閾值：';
  dict.zh['assets.tacmAlertsOverLb'] = 'CRIT 閾值：';
  dict.zh['assets.tacmAlertsOverDetail'] = '已超過 CRIT 閾值——接近或超過供應上限';
  dict.zh['assets.tacmAlertsNearDetail'] = '接近 WARN 閾值——剩餘可挖有限';
  dict.zh['assets.tacmAlertsOk'] = '開採進度健康';
  dict.zh['tiusd.alertsSecret'] = 'Webhook Secret（HMAC 簽名）：';
  dict.zh['oracle.native'] = '價格預言機（原生資產）';
  dict.zh['oracle.note'] = 'TACm 市價＝交易所最後成交（未上市按 1:0.330 兜底）；TiUSD 穩定幣 1:1 錨定 USDT。';
  dict.zh['oracle.asset'] = '資產';
  dict.zh['oracle.price'] = '價格 (USDT)';
  dict.zh['oracle.decimals'] = '精度';
  dict.zh['oracle.source'] = '來源';
  dict.zh['oracle.updated'] = '更新';
  dict.zh['mining.refs'] = '推薦';
  dict.zh['mining.refsUnit'] = '人';
  dict.zh['mining.inviteBonus'] = ' 推薦加成';
  dict.zh['assets.bridgeChainPending'] = '分鏈待處理明細';
  dict.zh['assets.bridgeChainName'] = '鏈';
  dict.zh['assets.bridgeChainId'] = 'Chain ID';
  dict.zh['assets.bridgeChainPending2'] = '待處理';

 dict.zh['assets.tabTacmSub'] = '原生幣 · 出塊獎勵'; dict.zh['assets.tabTiUSD'] = 'TiUSD'; dict.zh['assets.tabTiUSDSub'] = '穩定幣 · 1:1 錨定 USDT';
  dict.zh['assets.tacmOverview'] = 'TACm 總覽'; dict.zh['assets.mined'] = '已出塊產出（全鏈）'; dict.zh['assets.cap2'] = '總供應上限'; dict.zh['assets.remaining2'] = '剩餘可挖';
  dict.zh['assets.util2'] = '開採進度'; dict.zh['assets.health2'] = '膨脹檢查'; dict.zh['assets.tacmNote'] = 'TACm 為原生幣：出塊獎勵（coinbase）產生——節點 45% / 獎勵池 15% / 礦工 40%。';
  dict.zh['assets.reserves2'] = '儲備／鎖倉池'; dict.zh['assets.rewardPool'] = '鏈上獎勵池 (reward_pool)'; dict.zh['assets.cdpVault'] = 'CDP 抵押託管 (defi_cdp)'; dict.zh['assets.feePool2'] = '手續費池 (fee)';
  dict.zh['assets.flow2'] = 'TACm 流向統計（帳本分類）'; dict.zh['assets.category2'] = '來源 / 流向'; dict.zh['assets.inflow2'] = '流入 (TACm)'; dict.zh['assets.outflow2'] = '流出 (TACm)';
  dict.zh['assets.bridge2'] = '跨鏈轉出 / 轉入明細（TACm）'; dict.zh['assets.dir2'] = '方向'; dict.zh['assets.route2'] = '鏈路由'; dict.zh['assets.amount2'] = '金額'; dict.zh['assets.status2'] = '狀態'; dict.zh['assets.addr2'] = '地址'; dict.zh['assets.txhash2'] = '交易哈希';
  dict.zh['assets.recent2'] = '近期 TACm 流水（帳本）'; dict.zh['assets.time2'] = '時間'; dict.zh['assets.account2'] = '帳戶'; dict.zh['assets.amount3'] = '金額'; dict.zh['assets.memo2'] = 'Memo';
  dict.zh['assets.halving'] = '減半模式 (未設上限)';
  dict.zh['assets.f.cbNode'] = '節點獎勵 (45%)'; dict.zh['assets.f.cbPool'] = '獎勵池挹注 (15%)'; dict.zh['assets.f.cbMiner'] = '礦工獎勵 (40%)'; dict.zh['assets.f.swap'] = '兌換 (swap)'; dict.zh['assets.f.borrow'] = '借款 / 還款 (CDP)';
  dict.zh['assets.f.blockReward'] = '出塊獎勵 (coinbase)'; dict.zh['assets.f.staking'] = '質押 / 委託'; dict.zh['assets.f.cdp'] = 'CDP 抵押鎖倉'; dict.zh['assets.f.yieldFarm']='流動性挖礦';
  dict.zh['assets.f.ido']='IDO 發行 / 回購';
  dict.zh['assets.f.lp'] = '流動性 (LP)'; dict.zh['assets.f.bridge'] = '跨鏈橋'; dict.zh['assets.f.transfer'] = '一般轉帳'; dict.zh['api.tiusdDesc'] = 'TiUSD 穩定幣總覽：供應、市值上限、下限、利用率、儲備、流向、跨鏈淨額。'; dict.zh['api.histDesc'] = 'TiUSD 累計鑄造歷史＋現值上限。'; dict.zh['api.posDesc'] = '全鏈借款頭寸＋總借款／總抵押。'; dict.zh['api.csvDesc'] = 'TiUSD 全量帳本 CSV 匯出。'; dict.zh['api.tacmDesc'] = 'TACm 總覽：產出、上限、剩餘可挖、流向分類。'; dict.zh['api.alertsDesc'] = '最近 30 天鏈上警報事件。'; dict.zh['api.rpcdocDesc'] = '節點 RPC 全路由清單（自動生成）。'; dict.zh['api.full'] = '節點 RPC 全路由 / Full Route List'; dict.zh['api.fullNote'] = '節點自動註冊的全部 HTTP 路由（含 eth_* JSON-RPC 面）。'; dict.zh['api.fullFail'] = '路由載入失敗';
dict.zh['sdk.title'] = '開發者 SDK — 主網範例';
dict.zh['sdk.intro'] = 'TAC Ai 智能鏈官方 JS SDK（tacjs）：連線主網節點、查詢鏈狀態與地址餘額，並以一步轉帳示範簽署與提交。SDK 原始碼位於專案 sdk/tacjs（瀏覽器與 Node 通用）。';
dict.zh['sdk.rpcTitle'] = '節點連線測試';
dict.zh['sdk.rpcDesc'] = '輸入節點 RPC 位址（預設為目前站台），查詢鏈狀態。';
dict.zh['sdk.rpcBtn'] = '連線測試';
dict.zh['sdk.rpcFail'] = '連線失敗';
dict.zh['sdk.balTitle'] = '地址餘額查詢';
dict.zh['sdk.balDesc'] = '透過 Indexer 端點 GET /api/v1/address/{address} 查詢 TACm 餘額與交易筆數。';
dict.zh['sdk.balBtn'] = '查餘額';
dict.zh['sdk.balFail'] = '查詢失敗（主網需啟用 Indexer）';
dict.zh['sdk.balNeedAddr'] = '請輸入地址';
dict.zh['sdk.codeTitle'] = 'JS SDK 範例';
dict.zh['sdk.codeDesc'] = '與 Go SDK（tacclient）簽章格式互通——前端用 tacjs 簽署的交易，Go 節點可直接驗證入池。';
dict.zh['sdk.endpointTitle'] = '常用端點';
dict.zh['sdk.endpointDesc'] = '下列端點可由任何 JSON 客戶端直接呼叫。'; dict.zh['assets.warn'] = '⚠ 供應利用率 '; dict.zh['assets.warn2'] = ' — 接近上限 (>80%)'; dict.zh['assets.warnCrit'] = '⚠ 供應利用率 '; dict.zh['assets.warnCrit2'] = ' — 接近/超過市值上限，將觸發自動銷毀';

  dict.zh['cm.cancel'] = '取消'; dict.zh['cm.editProfile'] = '編輯個人資料'; dict.zh['cm.dispName'] = '對外顯示名稱';
  dict.zh['cm.avatar'] = '個人頭像（圖片網址或上傳後網址）'; dict.zh['cm.save'] = '儲存';
  dict.zh['tx.status'] = '狀態'; dict.zh['tx.confirmations'] = '確認數'; dict.zh['tx.blocks'] = '區塊';
  dict.zh['tx.block'] = '所在區塊'; dict.zh['tx.fee'] = '手續費'; dict.zh['tx.gasPrice'] = 'Gas Price（有效）';
  dict.zh['blk.back'] = '← 返回'; dict.zh['blk.hash'] = '區塊哈希'; dict.zh['blk.prev'] = '上一區塊'; dict.zh['blk.merkle'] = 'Merkle 根';
  dict.zh['blk.proposer'] = '提議者'; dict.zh['blk.difficulty'] = '當前難度'; dict.zh['blk.txcount'] = '交易數';
  dict.zh['blk.basefee'] = 'Base Fee'; dict.zh['blk.gasused'] = 'Gas Used'; dict.zh['blk.burned'] = '已銷毀 (Burned)';
  dict.zh['blk.ts'] = '出塊時間'; dict.zh['blk.txs'] = '區塊交易'; dict.zh['blk.txhash'] = '交易哈希';
  dict.zh['blk.from'] = '發送方'; dict.zh['blk.to'] = '接收方'; dict.zh['blk.amount'] = '金額'; dict.zh['blk.fee'] = '手續費';
  dict.zh['blk.empty'] = '本區塊無交易';
  dict.en['blk.back'] = '← Back'; dict.en['blk.hash'] = 'Block Hash'; dict.en['blk.prev'] = 'Previous Block'; dict.en['blk.merkle'] = 'Merkle Root';
  dict.en['blk.proposer'] = 'Proposer'; dict.en['blk.difficulty'] = 'Difficulty'; dict.en['blk.txcount'] = 'Transactions';
  dict.en['blk.basefee'] = 'Base Fee'; dict.en['blk.gasused'] = 'Gas Used'; dict.en['blk.burned'] = 'Burned';
  dict.en['blk.ts'] = 'Block Time'; dict.en['blk.txs'] = 'Block Transactions'; dict.en['blk.txhash'] = 'Tx Hash';
  dict.en['blk.from'] = 'From'; dict.en['blk.to'] = 'To'; dict.en['blk.amount'] = 'Amount'; dict.en['blk.fee'] = 'Fee';
  dict.en['blk.empty'] = 'No transactions in this block';
  dict.zh['cm.loading'] = '載入中…';
  dict.en['tiusd.title'] = 'TiUSD Stablecoin Monitor'; dict.en['tiusd.sub'] = 'Reserves · Mint · Circulation · Flow · Cross-chain details — ensure supply never over-expands';
  dict.en['tiusd.overview'] = 'Overview'; dict.en['tiusd.supply'] = 'Circulating Supply (TiUSD)'; dict.en['tiusd.cap'] = 'Mcap Cap (1:1 mcap)'; dict.en['tiusd.mcap'] = 'Mcap Rule'; dict.en['tiusd.listed'] = 'Listed · price $'; dict.en['tiusd.unlisted'] = 'Unlisted · 1:0.330 basis';
  dict.en['tiusd.floor'] = 'Supply Floor (33%)'; dict.en['tiusd.remaining'] = 'Remaining Mintable'; dict.en['tiusd.util'] = 'Utilization';
  dict.en['tiusd.peg'] = 'Peg Price'; dict.en['tiusd.health'] = 'Inflation Check'; dict.en['tiusd.h.unlisted'] = 'Unlisted · 0.330 basis'; dict.en['tiusd.export'] = 'Export Ledger CSV'; dict.en['tiusd.chart'] = 'Supply vs Cap Trend'; dict.en['tiusd.chartNote'] = 'Cumulative mint (ledger) vs market cap — inflation check.'; dict.en['tiusd.chartWait'] = 'No mint history yet'; dict.en['tiusd.pie'] = 'Supply Sources'; dict.en['tiusd.positions'] = 'Loan Positions (on-chain DeFi)'; dict.en['tiusd.addr2'] = 'Address'; dict.en['tiusd.collateral'] = 'Collateral'; dict.en['tiusd.borrow2'] = 'Borrowed'; dict.en['tiusd.status2'] = 'Status'; dict.en['tiusd.noPos'] = 'No loans yet'; dict.en['tiusd.posTotals'] = 'Total borrow:'; dict.en['tiusd.posColl'] = 'Total collateral:'; dict.en['tiusd.posActive'] = 'Active:'; dict.en['tiusd.bridgeNet'] = 'Bridge net inflow (in−out):'; dict.en['tiusd.bridgeDone'] = 'completed'; dict.en['tiusd.alerts'] = 'Recent Alerts (30d)'; dict.en['tiusd.alertsLevel'] = 'Level'; dict.en['tiusd.alertsType'] = 'Type'; dict.en['tiusd.alertsDetail'] = 'Detail'; dict.en['tiusd.alertsTime'] = 'Time';  dict.en['tiusd.alertsCfg'] = '⚙ Config';
 dict.en['tiusd.alertsCfgTitle'] = 'Alert Thresholds';
  dict.en['tiusd.alertsNear'] = 'WARN threshold (0-1):';
  dict.en['tiusd.alertsOver'] = 'CRIT threshold (0-1):';
  dict.en['tiusd.alertsWebhook'] = 'Webhook URL:';
  dict.en['tiusd.alertsWebhookNote'] = 'POSTs JSON ({event:\'tiusd_alert\', alerts:[...]}) to this URL when an alert fires; leave empty to disable.';
  dict.en['tiusd.alertsSaved'] = 'Saved';
  dict.en['tiusd.alertsFail'] = 'Failed';
  dict.en['tiusd.cancel'] = 'Cancel';
  dict.en['tiusd.save'] = 'Save';
 dict.en['tiusd.bridgeStatus'] = 'Bridge Status';
  dict.en['tiusd.bridgePending'] = 'Pending';
  dict.en['tiusd.bridgeChains'] = 'Chains';
  dict.en['tiusd.bridgeFee'] = 'Fee Rate';
  dict.en['tiusd.bridgePaused'] = 'Paused';
 dict.en['tiusd.poolOps'] = 'Pool Recent Ops (TiUSD)';
  dict.en['tiusd.poolAcct'] = 'Pool Account';
  dict.en['tiusd.memo'] = 'Memo';
  dict.en['tiusd.yes'] = 'Yes';
  dict.en['tiusd.no'] = 'No';
  dict.en['tiusd.time'] = 'Time';
dict.en['tiusd.alertsNone'] = 'No alerts'; dict.en['tiusd.reserves'] = 'Real Reserves';
  dict.en['tiusd.stablePool'] = 'Swap Reserve Pool (stable-pool)'; dict.en['tiusd.feePool'] = 'Fee Pool';
  dict.en['tiusd.reserveNote'] = 'TiUSD is convertible only when backed by real reserves (USDT/TACm); otherwise TACm flash-swap only.';
  dict.en['tiusd.mint'] = 'Mint & Burn'; dict.en['tiusd.totalMinted'] = 'Total Minted'; dict.en['tiusd.totalBurned'] = 'Total Burned';
  dict.en['tiusd.net'] = 'Net Minted'; dict.en['tiusd.updated'] = 'Updated';
  dict.en['tiusd.mintNote'] = 'Ways to mint: CDP collateral loan, native-swap TACm flash-swap, stable-swap (reserve-limited), supply floor top-up; repay burns.';
  dict.en['tiusd.flow'] = 'TiUSD Flow by Ledger Category'; dict.en['tiusd.category'] = 'Source / Flow'; dict.en['tiusd.inflow'] = 'Inflow (TiUSD)';
  dict.en['tiusd.outflow'] = 'Outflow (TiUSD)'; dict.en['tiusd.net2'] = 'Net';
  dict.en['tiusd.bridgeTitle'] = 'Cross-chain Transfer Details (TiUSD)'; dict.en['tiusd.dir'] = 'Direction'; dict.en['tiusd.route'] = 'Chain Route';
  dict.en['tiusd.amount'] = 'Amount'; dict.en['tiusd.status'] = 'Status'; dict.en['tiusd.addr'] = 'Address'; dict.en['tiusd.txhash'] = 'Tx Hash';
  dict.en['tiusd.recent'] = 'Recent TiUSD Ledger'; dict.en['tiusd.time'] = 'Time'; dict.en['tiusd.account'] = 'Account'; dict.en['tiusd.memo'] = 'Memo';
  dict.en['tiusd.na'] = 'Disabled (no TACM_MAX_SUPPLY)'; dict.en['tiusd.h.ok'] = 'Healthy · Within Cap'; dict.en['tiusd.h.near'] = 'Near Cap (>95%)';
  dict.en['tiusd.h.over'] = 'OVER CAP!'; dict.en['tiusd.h.dis'] = 'Cap Disabled (not set)';
  dict.en['tiusd.f.cdp'] = 'CDP Collateral Mint'; dict.en['tiusd.f.repay'] = 'Repay / Burn'; dict.en['tiusd.f.stable'] = 'Stable Swap';
  dict.en['tiusd.f.native'] = 'Native Swap (TACm)'; dict.en['tiusd.f.seed'] = 'Pool Genesis Seed (on-chain mint)'; dict.en['tiusd.f.floor'] = 'Supply Floor Top-up';
  dict.en['tiusd.f.bridge'] = 'Cross-chain Bridge'; dict.en['tiusd.f.transfer'] = 'Transfer'; dict.en['tiusd.f.other'] = 'Other (uncategorized)'; dict.en['assets.f.other'] = 'Other (uncategorized)';
  dict.en['tiusd.din'] = 'IN'; dict.en['tiusd.dout'] = 'OUT';
  dict.en['tiusd.noBridge'] = 'No TiUSD bridge records yet'; dict.en['tiusd.noLedger'] = 'No TiUSD ledger entries yet';
  dict.en['assets.title'] = 'Assets Center'; dict.en['assets.sub'] = 'On-chain asset overview: supply · reserves · flow · cross-chain details — no over-expansion';
  dict.en['assets.tabTacm'] = 'TACm'; dict.en['assets.export'] = 'Export Ledger CSV';
  dict.en['assets.tools'] = 'Developer Tools';
  dict.en['assets.signTool'] = 'Sign Tool (EIP-191/712)';
  dict.en['assets.tokenStudio'] = 'Token Studio';
  dict.en['assets.contractVerify'] = 'Contract Verifier';
  dict.en['assets.blockExplorer'] = 'Block Explorer';
  dict.en['assets.rpcDocs'] = 'API Docs';
dict.en['assets.secTitle'] = 'Wallet Security & Multisig';
dict.en['assets.secNote'] = 'EIP-191 / EIP-712 standard signing and MetaMask-grade multi-asset management are supported; Multisig uses threshold signatures where a transaction requires a configured number of co-signers.';
dict.en['assets.secList'] = '• Signing tool: message / TypedData sign & verify<br>• Multisig: create 2/3, 3/5 multisig accounts in the Web3 wallet<br>• Hardware wallet: Ledger-style offline signing (signature generated locally, private key never leaves device)';
  dict.en['assets.tacmPrice'] = 'Market Price';
  dict.en['assets.tacmUpdated'] = 'Updated';
  dict.en['assets.tacmPoolOps'] = 'Pool Recent Ops (TACm)';
  dict.en['assets.poolAcct2'] = 'Pool Account';
  dict.en['assets.tacmTrend'] = 'Mined vs Supply Cap Trend';
  dict.en['assets.tacmTrendNote'] = 'Cumulative mined (ledger mint/reward) vs supply cap — mining progress check.';
  dict.en['assets.tacmPie'] = 'Mining Sources';
  dict.en['assets.tacmAlerts'] = 'Mining Progress Alerts';
  dict.en['assets.tacmBridge'] = 'Bridge Status';
  dict.en['assets.tacmAlertsDisabled'] = 'No cap (halving mode) — alerts disabled';
  dict.en['assets.tacmAlertsSummary'] = 'Mining progress:';
  dict.en['assets.tacmAlertsNearLb'] = 'WARN:';
  dict.en['assets.tacmAlertsOverLb'] = 'CRIT:';
  dict.en['assets.tacmAlertsOverDetail'] = 'Over CRIT threshold — near/over supply cap';
  dict.en['assets.tacmAlertsNearDetail'] = 'Near WARN threshold — limited remaining';
  dict.en['assets.tacmAlertsOk'] = 'Mining progress healthy';
  dict.en['tiusd.alertsSecret'] = 'Webhook Secret (HMAC sign):';
  dict.en['oracle.native'] = 'Price Oracle (Native Assets)';
  dict.en['oracle.note'] = 'TACm = last exchange trade (unlisted: 1:0.330 floor); TiUSD pegged 1:1 to USDT.';
  dict.en['oracle.asset'] = 'Asset';
  dict.en['oracle.price'] = 'Price (USDT)';
  dict.en['oracle.decimals'] = 'Decimals';
  dict.en['oracle.source'] = 'Source';
  dict.en['oracle.updated'] = 'Updated';
  dict.en['mining.refs'] = 'Referrals';
  dict.en['mining.refsUnit'] = '';
  dict.en['mining.inviteBonus'] = ' referral bonus';
  dict.en['assets.bridgeChainPending'] = 'Per-chain Pending';
  dict.en['assets.bridgeChainName'] = 'Chain';
  dict.en['assets.bridgeChainId'] = 'Chain ID';
  dict.en['assets.bridgeChainPending2'] = 'Pending';

 dict.en['assets.tabTacmSub'] = 'Native coin · block rewards'; dict.en['assets.tabTiUSD'] = 'TiUSD'; dict.en['assets.tabTiUSDSub'] = 'Stablecoin · 1:1 USDT peg';
  dict.en['assets.tacmOverview'] = 'TACm Overview'; dict.en['assets.mined'] = 'Mined (chain-wide)'; dict.en['assets.cap2'] = 'Total Supply Cap'; dict.en['assets.remaining2'] = 'Remaining to Mine';
  dict.en['assets.util2'] = 'Mining Progress'; dict.en['assets.health2'] = 'Inflation Check'; dict.en['assets.tacmNote'] = 'TACm is the native coin: minted via block rewards (coinbase) — node 45% / pool 15% / miner 40%.';
  dict.en['assets.reserves2'] = 'Reserves / Locked'; dict.en['assets.rewardPool'] = 'On-chain Reward Pool (reward_pool)'; dict.en['assets.cdpVault'] = 'CDP Collateral Vault (defi_cdp)'; dict.en['assets.feePool2'] = 'Fee Pool (fee)';
  dict.en['assets.flow2'] = 'TACm Flow by Ledger Category'; dict.en['assets.category2'] = 'Source / Flow'; dict.en['assets.inflow2'] = 'Inflow (TACm)'; dict.en['assets.outflow2'] = 'Outflow (TACm)';
  dict.en['assets.bridge2'] = 'Cross-chain Transfer Details (TACm)'; dict.en['assets.dir2'] = 'Direction'; dict.en['assets.route2'] = 'Chain Route'; dict.en['assets.amount2'] = 'Amount'; dict.en['assets.status2'] = 'Status'; dict.en['assets.addr2'] = 'Address'; dict.en['assets.txhash2'] = 'Tx Hash';
  dict.en['assets.recent2'] = 'Recent TACm Ledger'; dict.en['assets.time2'] = 'Time'; dict.en['assets.account2'] = 'Account'; dict.en['assets.amount3'] = 'Amount'; dict.en['assets.memo2'] = 'Memo';
  dict.en['assets.halving'] = 'Halving Mode (no cap set)';
  dict.en['assets.f.cbNode'] = 'Node Reward (45%)'; dict.en['assets.f.cbPool'] = 'Reward Pool (15%)'; dict.en['assets.f.cbMiner'] = 'Miner Reward (40%)'; dict.en['assets.f.swap'] = 'Swap'; dict.en['assets.f.borrow'] = 'Borrow / Repay (CDP)';
  dict.en['assets.f.blockReward'] = 'Block Reward (coinbase)'; dict.en['assets.f.staking'] = 'Staking / Delegation'; dict.en['assets.f.cdp'] = 'CDP Collateral Lock'; dict.en['assets.f.yieldFarm']='Liquidity Farm';
  dict.en['assets.f.ido']='IDO Launch / Repurchase';
  dict.en['assets.f.lp'] = 'Liquidity (LP)'; dict.en['assets.f.bridge'] = 'Cross-chain Bridge'; dict.en['assets.f.transfer'] = 'Transfer'; dict.en['api.tiusdDesc'] = 'TiUSD stablecoin overview: supply, mcap cap, floor, utilization, reserves, flow, bridge net.'; dict.en['api.histDesc'] = 'TiUSD cumulative mint history + current cap.'; dict.en['api.posDesc'] = 'All-chain loan positions + totals.'; dict.en['api.csvDesc'] = 'TiUSD full ledger CSV export.'; dict.en['api.tacmDesc'] = 'TACm overview: mined, cap, remaining, flow categories.'; dict.en['api.alertsDesc'] = 'On-chain alert events (last 30 days).'; dict.en['api.rpcdocDesc'] = 'Full node RPC route list (auto-generated).'; dict.en['api.full'] = 'Node RPC Full Route List'; dict.en['api.fullNote'] = 'All auto-registered HTTP routes (incl. eth_* JSON-RPC).'; dict.en['api.fullFail'] = 'Failed to load routes';
dict.en['sdk.title'] = 'Developer SDK — Mainnet Examples';
dict.en['sdk.intro'] = 'Official JS SDK (tacjs) for TAC Ai Smart Chain: connect to a mainnet node, query chain status and address balances, and demo signing & submission with one-step transfers. Source lives in sdk/tacjs (browser + Node).';
dict.en['sdk.rpcTitle'] = 'Node Connection Test';
dict.en['sdk.rpcDesc'] = 'Enter a node RPC URL (defaults to this site) to query chain status.';
dict.en['sdk.rpcBtn'] = 'Test';
dict.en['sdk.rpcFail'] = 'Connection failed';
dict.en['sdk.balTitle'] = 'Address Balance';
dict.en['sdk.balDesc'] = 'Query TACm balance and tx count via Indexer endpoint GET /api/v1/address/{address}';
dict.en['sdk.balBtn'] = 'Query';
dict.en['sdk.balFail'] = 'Query failed (mainnet needs Indexer enabled)';
dict.en['sdk.balNeedAddr'] = 'Enter an address';
dict.en['sdk.codeTitle'] = 'JS SDK Usage';
dict.en['sdk.codeDesc'] = 'Interoperable with the Go SDK (tacclient): transactions signed by tacjs in the frontend are directly verifiable by Go nodes.';
dict.en['sdk.endpointTitle'] = 'Common Endpoints';
dict.en['sdk.endpointDesc'] = 'The following endpoints can be called by any JSON client.'; dict.en['assets.warn'] = '⚠ Utilization '; dict.en['assets.warn2'] = ' — approaching cap (>80%)'; dict.en['assets.warnCrit'] = '⚠ Utilization '; dict.en['assets.warnCrit2'] = ' — near/over cap, auto-burn may trigger';

  dict.en['cm.cancel'] = 'Cancel'; dict.en['cm.editProfile'] = 'Edit Profile'; dict.en['cm.dispName'] = 'Display Name';
  dict.en['cm.avatar'] = 'Avatar (image URL or uploaded URL)'; dict.en['cm.save'] = 'Save';
  dict.en['tx.status'] = 'Status'; dict.en['tx.confirmations'] = 'Confirmations'; dict.en['tx.blocks'] = 'blocks';
  dict.en['tx.block'] = 'Block'; dict.en['tx.fee'] = 'Fee'; dict.en['tx.gasPrice'] = 'Gas Price (effective)';
  dict.en['cm.loading'] = 'Loading…';
  zh2en['填寫邀請人地址'] = 'Enter inviter address'; zh2en['邀請人礦機算力'] = 'Inviter miner hashrate';
  zh2en['區塊交易'] = 'Block Transactions'; zh2en['已銷毀 (Burned)'] = 'Burned'; zh2en['已銷毀'] = 'Burned';
  zh2en['場外交易'] = 'OTC Trading'; zh2en['法幣直接買賣加密貨幣'] = 'Fiat crypto trading';
  zh2en['買幣'] = 'Buy Crypto'; zh2en['賣幣'] = 'Sell Crypto';
  zh2en['智能鏈'] = 'Smart Chain'; zh2en['管理儀表板'] = 'Admin Dashboard';
  zh2en['加入 TAC Ai 智能鏈主網'] = 'Join TAC Ai Smart Chain Mainnet';
  zh2en['借出 TiUSD'] = 'Borrow TiUSD'; zh2en['倉位'] = 'Position'; zh2en['可借上限'] = 'Borrow Limit';
  zh2en['安全審計'] = 'Security Audit';
  zh2en['提領節點獎勵'] = 'Withdraw Node Rewards'; zh2en['提領到綁定錢包'] = 'Withdraw to Bound Wallet';
  zh2en['入帳筆數（近 200 塊）'] = 'Credits (last 200 blocks)'; zh2en['心跳上鏈（近 200 塊）'] = 'Heartbeats on-chain (last 200 blocks)';
  zh2en['節點狀態'] = 'Node Status'; zh2en['節點位址'] = 'Node Address'; zh2en['節點 ID'] = 'Node ID';
  // M150g-2：內容段落級 i18n——c2c/defi/join 長文段落補 zh2en（英文模式全站英文化）。
  zh2en['法幣直接買賣加密貨幣 · 廣告主擔保交易 · 支持'] = 'Fiat buy/sell crypto · escrowed by advertisers · ';
  zh2en['身份（錢包地址）'] = 'Identity (wallet address)';
  zh2en['以地址為身份操作'] = 'Address-as-identity';
  zh2en['資產進出即時上鏈記帳（費率：'] = 'Assets on-chain instantly (fee: ';
  zh2en['無常損失風險（'] = 'Impermanent loss risk (';
  zh2en['提供流動性後，若代幣相對價格大幅波動，退出時資產價值可能低於單純持有兩種代幣。高波動交易對風險更大；手續費與挖礦獎勵可部分彌補。'] = 'Providing liquidity, if token prices move sharply the exit value may be below simply holding both tokens. High-volatility pairs risk more; fees and mining rewards partially compensate.';
  zh2en['估算公式（價格比'] = 'Formula (price ratio ';
  zh2en['我已了解並接受無常損失風險'] = 'I understand and accept impermanent loss risk';
  zh2en['份額即池內權益'] = 'Share = pool equity';
  zh2en['鏈上可驗證'] = 'On-chain verifiable';
  zh2en['審計項目'] = 'Audit Items';
  zh2en['核心鏈一致性（'] = 'Core chain consistency (';
  zh2en['全網複驗）'] = 'network re-verification)';
  zh2en['已審計'] = 'Audited';
  zh2en['原子性與'] = 'Atomicity & ';
  zh2en['資源治理'] = 'resource governance';
  zh2en['跨鏈橋多簽'] = 'Bridge multi-sig';
  zh2en['頭鏈驗證'] = 'Header-chain verification';
  zh2en['全鏈帳本重放（'] = 'Full-ledger replay (';
  zh2en['第三方機構級審計（'] = 'Third-party institutional audit (';
  zh2en['進行中'] = 'In Progress';
  zh2en['方法可鏈上複驗：瀏覽器交易明細、'] = 'Methods on-chain verifiable: browser tx details, ';
  zh2en['跨鏈橋事件日誌皆公開。'] = 'bridge event logs are public.';
  zh2en['抵押借款（抵押資產 × 抵押率'] = 'Collateral loan (collateral × ratio ';
  zh2en['最大可借）'] = 'max borrowable)';
  zh2en['年息、全額還款釋放抵押'] = 'annual interest, full repayment releases collateral';
  zh2en['超額抵押 · 抵押'] = 'Over-collateralized · collateral';
  zh2en['開倉（抵押率'] = 'Open position (ratio ';
  zh2en['清算線 130% · 穩定費年息 2% · 清算罰金 5%）'] = 'Liquidation 130% · stability fee 2% p.a. · penalty 5%)';
  zh2en['智能鏈主網'] = 'Smart Chain Mainnet';
  zh2en['任何一台伺服器都可成為節點（'] = 'Any server can become a node (';
  zh2en['與創世節點同步同一條鏈、共享全網狀態；內建礦機可額外挖礦瓜分出塊獎勵。節點之間嚴格遵循同一套出塊獎勵規則，任何入口看到的鏈上資料與在線礦工完全一致。'] = 'Syncs the same chain as the genesis node, sharing global state; built-in miner can additionally mine block rewards. All nodes follow identical reward rules — every entry shows the same on-chain data and online miners.';
  zh2en['網絡概況'] = 'Network Overview';
  zh2en['分潤（池'] = 'split (pool ';
  zh2en['礦工）'] = 'miners)';
  zh2en['為實驗網段，無固定公開'] = 'testnet segment, no fixed public ';
  zh2en['由創世節點以 -'] = 'genesis node announces via -';
  zh2en['自行播報'] = 'self-announced';
  zh2en['尚無公開'] = 'no public ';
  zh2en['方式一：下載節點程式包（免編譯，推薦）'] = 'Option 1: Download node binary (no compile, recommended)';
  zh2en['官方預編譯的'] = 'officially pre-compiled ';
  zh2en['純節點程式'] = 'pure node binary';
  zh2en['不含錢包/交易所/社群等'] = 'no wallet/exchange/community included';
  zh2en['下載即用，不需'] = 'download & run, no ';
  zh2en['不需編譯、不需整套系統原始碼。'] = 'no compile, no full source needed.';
  zh2en['啟動節點（把'] = 'Start node (replace ';
  zh2en['換成自己的公網'] = 'with your public ';
  zh2en['永久運行（'] = 'run forever (';
  zh2en['開機自啟）：'] = 'auto-start):';
  zh2en['首次啟動自動生成節點金鑰（'] = 'First start auto-generates node key (';
  zh2en['你的收款錢包，出塊獎勵自動入帳。'] = 'your payout wallet, block rewards auto-credited.';
  zh2en['請立即備份：'] = 'Backup now:';
  zh2en['方式二：一鍵自動安裝（需'] = 'Option 2: One-command auto install (needs ';
  zh2en['編譯）'] = 'compile)';
  zh2en['一行一鍵安裝（自動下載'] = 'One-liner install (auto-download ';
  zh2en['啟動）。'] = 'start).';
  zh2en['使用獨立鏈'] = 'use standalone chain';
  zh2en['一行一鍵搭建'] = 'one-liner setup';
  zh2en['自動裝'] = 'auto-install';
  zh2en['偵測公網'] = 'detect public ';
  zh2en['伺服器用'] = 'server uses';
  zh2en['開機自啟），手機'] = 'auto-start), mobile ';
  zh2en['自動用'] = 'auto-use';
  zh2en['自訂埠加'] = 'custom port +';
  zh2en['指定模式加'] = 'specified mode +';
  zh2en['啟動後節點品牌控制台'] = 'after start, node-branded console';
  zh2en['此腳本為早期版本（需'] = 'This script is an early version (needs ';
  zh2en['主網現行請優先使用上方'] = 'mainnet: prefer ';
  zh2en['節點程式包。'] = 'node binary above.';
  zh2en['節點要求'] = 'Node Requirements';
  zh2en['建議 2'] = 'Recommended 2';
  zh2en['以上（鏈資料會持續增長）'] = 'or more (chain data grows)';
  zh2en['同埠合併）；8081 埠供節點品牌控制台'] = 'same-port merge); port 8081 for node console';
  zh2en['首次啟動自動從'] = 'first start auto-syncs from ';
  zh2en['節點同步全鏈'] = 'node full-chain sync';
  zh2en['質押成為節點（去中心化共識）'] = 'Stake to become node (decentralized consensus)';
  zh2en['創節點引導獎勵（限額 50 名）'] = 'Genesis node onboarding reward (limited to 50)';
  zh2en['新節點首次上線可領取'] = 'New nodes on first launch can claim ';
  zh2en['自動質押、直接成為節點'] = 'auto-stake, instantly become node';
  zh2en['資金來自創世社群基金，'] = 'Funded by genesis community fund, ';
  zh2en['每地址僅一次、限量 50 名'] = 'once per address, limited 50';
  zh2en['額滿即止）。領取方式：'] = 'first come first served). Claim:';
  zh2en['返回交易雜湊即領取成功；入塊後 1'] = 'Return tx hash to claim; 1 min after block';
  zh2en['入錢包並自動鎖入質押池。'] = 'into wallet & auto-locked to staking pool.';
  zh2en['鎖幣取得節點資格'] = 'Lock to become node';
  zh2en['與主流'] = 'like mainstream ';
  zh2en['以太坊等）一致：節點把'] = 'Ethereum): node locks ';
  zh2en['鎖進鏈上質押池'] = 'into on-chain staking pool';
  zh2en['進入節點集（每 60 秒由鏈上質押重放重建，全網一致）'] = 'enter node set (rebuilt every 60s from on-chain stakes, globally consistent)';
  zh2en['輪值出塊、由在線節點均分'] = 'round-robin blocks, split among online nodes';
  zh2en['節點獎勵'] = 'Node Rewards';
  zh2en['節點越多、出塊越分散，鏈越去中心化（最多'] = 'More nodes = more decentralization (max ';
  zh2en['同時輪值）。'] = 'rotating simultaneously).';
  zh2en['門檻：質押'] = 'Threshold: stake ';
  zh2en['質押高者優先入集）。質押幣鎖在鏈上質押池（鏈上可查），不可挪用。'] = 'higher stake enters first). Staked coins locked on-chain (verifiable), not withdrawable.';
  zh2en['第 1 步：確保節點錢包有幣'] = 'Step 1: Ensure node wallet has funds';
  zh2en['新節點初始餘額為 0，需先由其他持有者轉入（例如從創世節點錢包或交易所轉到你的節點地址）。節點地址'] = 'New nodes start at 0; transfer from holders (e.g. genesis wallet or exchange). Node address ';
  zh2en['節點金鑰對應地址，見節點面板'] = 'node key address, see node panel';
  zh2en['第 2 步：發質押交易'] = 'Step 2: Send stake tx';
  zh2en['在節點控制台（節點面板）點'] = 'In node console (node panel) click ';
  zh2en['質押成為節點'] = 'Stake as a Node';
  zh2en['輸入金額，或直接呼叫'] = 'Enter amount, or call ';
  zh2en['成功後返回交易雜湊；交易入塊後最多 1 分鐘，節點自動進入節點集並開始輪值出塊。'] = 'On success a tx hash returns; within 1 min the node auto-enters the node set and starts producing blocks.';
  zh2en['第 3 步：領取獎勵'] = 'Step 3: Claim rewards';
  zh2en['成為節點後：每塊'] = 'After becoming node: each block ';
  zh2en['由鏈上在線節點均分、直接入你的節點錢包（'] = 'split by online nodes, paid to your node wallet (';
  zh2en['另可開啟算力成為礦工，按在線算力瓜分'] = 'or enable hashrate to mine, split by online hashrate ';
  zh2en['礦工獎勵（'] = 'miner rewards (';
  zh2en['兩者可同時開啟。'] = 'Both can be enabled together.';
  zh2en['快照快速同步（重要）'] = 'Snapshot fast sync (important)';
  zh2en['節點啟動時加'] = 'When starting node add ';
  zh2en['會直接下載創世節點的最新狀態快照（創世'] = 'directly downloads genesis latest snapshot (';
  zh2en['全帳戶餘額），'] = 'all balances),';
  zh2en['秒級就緒'] = 'ready in seconds';
  zh2en['不再從創世逐塊重放（省 10 分鐘以上）。'] = 'no full genesis replay (saves 10+ min).';
  zh2en['無磁碟的免費雲節點（'] = 'Diskless free cloud nodes (';
  zh2en['等）每次重啟都必須帶此參數'] = 'etc.) must pass this flag on every restart';
  zh2en['否則回到創世重放、長時間落後。'] = 'otherwise replays from genesis and lags.';
  zh2en['多節點注意事項'] = 'Multi-node notes';
  zh2en['共識需 2/3 節點在線才持續出塊：'] = 'Consensus requires 2/3 nodes online:';
  zh2en['個節點時任一離線鏈即暫停'] = 'nodes: any offline pauses the chain';
  zh2en['建議節點達'] = 'Recommend reaching ';
  zh2en['個以上'] = 'or more nodes';
  zh2en['容忍 1 個離線）。加入的節點越多，鏈的可用性與去中心化程度越高。'] = 'tolerates 1 offline). More nodes = higher availability & decentralization.';
  zh2en['驗證與挖礦'] = 'Validation & Mining';
  zh2en['啟動後在網頁'] = 'After start, on web ';
  zh2en['頁開啟礦機即可參與出塊：收益按鏈上'] = 'page enable miner to mine: rewards by on-chain ';
  zh2en['分潤規則發放（池'] = 'split rules (pool ';
  zh2en['礦工），開機/關機狀態經心跳交易寫入鏈上，任何入口看到你的在線狀態一致。'] = 'miners); on/off state via heartbeat on-chain, consistent everywhere.';
  zh2en['原始碼永久固化（'] = 'Source permanently frozen (';
  zh2en['節點與整套鏈的原始碼固化到'] = 'Node & chain source frozen to ';
  zh2en['任何人可'] = 'Anyone can ';
  zh2en['審計、部署（一次一條）：'] = 'audit, deploy (one at a time):';
  zh2en['推完後將'] = 'After push, paste ';
  zh2en['公開倉庫網址'] = 'public repo URL ';
  zh2en['貼回本頁'] = 'back here';
  zh2en['列表（或告知我），即可成為全網永久可查證的開源創世節點。'] = 'list (or tell me) to become a permanently verifiable open-source genesis node.';
  zh2en['節點地址'] = 'Node Address';
  zh2en['編譯），主網現行請優先使用上方'] = 'compile), prefer the ';
  zh2en['方式一'] = 'Option 1';
  var en2zh = reverseMap(zh2en);
  // M88：錢包安全（助記詞/密碼鎖/簽名預覽/交易分頁）
  zh2en['助記詞備份'] = 'Mnemonic Backup';
  zh2en['導入助記詞'] = 'Restore Mnemonic';
  zh2en['設定密碼鎖'] = 'Set Passcode Lock';
  zh2en['解鎖'] = 'Unlock';
  zh2en['交易紀錄'] = 'Transaction History';
  zh2en['直播'] = 'Live'; zh2en['搜尋貼文…'] = 'Search posts...'; zh2en['首頁'] = 'Home';
  zh2en['影片'] = 'Videos'; zh2en['好友'] = 'Friends'; zh2en['通知'] = 'Notifications'; zh2en['個人'] = 'Profile';
  zh2en['市集商品'] = 'Marketplace'; zh2en['動態'] = 'Post'; zh2en['影片請填網址'] = 'Video URL required';
  zh2en['市集請填價格'] = 'Set a price for marketplace'; zh2en['內容不可為空'] = 'Content required';
  zh2en['影片/直播 kind 時顯示影片網址輸入'] = '';
  zh2en['匯出 CSV'] = 'Export CSV';
  zh2en['上一頁'] = 'Prev';
  zh2en['下一頁'] = 'Next';
  zh2en['全部'] = 'All';
  zh2en['轉入'] = 'In';
  zh2en['轉出'] = 'Out';
  zh2en['簽名預覽'] = 'Signature Preview';
  zh2en['確認送出'] = 'Confirm & Send';
  zh2en['取消'] = 'Cancel';
  zh2en['請輸入密碼'] = 'Enter passcode';
  zh2en['密碼錯誤'] = 'Wrong passcode';
  zh2en['請先解鎖錢包'] = 'Unlock wallet first';
  zh2en['錢包已鎖定'] = 'Wallet locked';
  zh2en['已解鎖'] = 'Unlocked';
  zh2en['錢包密碼鎖（防止旁人查看私鑰，非加密金鑰庫）'] = 'Passcode lock (hides keys from others; not an encrypted vault)';
  zh2en['助記詞為 24 個英文單詞，等同私鑰完整備份；請抄寫並妥善保管，勿上傳或截圖外傳'] = 'The 24-word mnemonic equals your private key; write it down safely. Never upload or screenshot it.';
  zh2en['助記詞無效'] = 'Invalid mnemonic';
  zh2en['交易紀錄（鏈上）'] = 'On-chain Transactions';
  zh2en['篩選'] = 'Filter';
  zh2en['無交易紀錄'] = 'No transactions yet.';
  zh2en['★ 評價'] = '★ Review';
  zh2en['送出評價'] = 'Submit Review';
  zh2en['評分（1-5）'] = 'Rating (1-5)';
  zh2en['★★★★★ 5 — 非常好'] = '★★★★★ 5 — Excellent';
  zh2en['★★★★ 4 — 好'] = '★★★★ 4 — Good';
  zh2en['★★★ 3 — 普通'] = '★★★ 3 — Average';
  zh2en['★★ 2 — 不佳'] = '★★ 2 — Poor';
  zh2en['★ 1 — 很差'] = '★ 1 — Bad';
  zh2en['Comment (optional)'] = 'Comment (optional)';
  zh2en[' 在想些什麼？'] = " What's on your mind?";
  zh2en['↗ 分享'] = '↗ Share';
  zh2en['已複製貼文連結'] = 'Post link copied';
  zh2en['請先勾選「我已了解並接受無常損失風險」'] = 'Please check "I understand and accept the impermanent loss risk" first';

  function sortedKeys(m) { return Object.keys(m).sort(function (a, b) { return b.length - a.length; }); }
  var zhKeys = sortedKeys(zh2en);
  var enKeys = sortedKeys(en2zh);
  var KEY = 'tac_lang';
  function cur() {
    try {
      var p = new URLSearchParams(window.location.search);
      var q = p.get('lang');
      if (q === 'en' || q === 'zh') { return q; }
      return localStorage.getItem(KEY) || 'en';
    } catch (e) { return 'en'; }
  }
  function replaceTextNodes(map, keys) {
    try {
      var walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT, {
        acceptNode: function (node) {
          var p = node.parentNode;
          while (p) {
            var tag = p.nodeName;
            if (tag === 'SCRIPT' || tag === 'STYLE') { return NodeFilter.FILTER_REJECT; }
            p = p.parentNode;
          }
          return NodeFilter.FILTER_ACCEPT;
        }
      });
      var nodes = [];
      while (walker.nextNode()) { nodes.push(walker.currentNode); }
      for (var i = 0; i < nodes.length; i++) {
        var n = nodes[i];
        var t = n.nodeValue;
        if (!t) continue;
        var changed = false;
        for (var j = 0; j < keys.length; j++) {
          if (t.indexOf(keys[j]) !== -1) { t = t.split(keys[j]).join(map[keys[j]]); changed = true; }
        }
        if (changed) { n.nodeValue = t; }
      }
    } catch (e) { /* 忽略 */ }
  }
  function apply() {
    var lang = cur();
    var d = dict[lang] || dict.en;
    var els = document.querySelectorAll('[data-i18n]');
    for (var i = 0; i < els.length; i++) {
      var k = els[i].getAttribute('data-i18n');
      if (d[k]) { els[i].textContent = d[k]; }
    }
    // M72：document.title 翻譯（模板 block title 可能含中文）。
    if (document.title && typeof document.title === 'string') {
      var tt = document.title;
      var changedT = false;
      var tkeys = lang === 'en' ? zhKeys : enKeys;
      var tmap = lang === 'en' ? zh2en : en2zh;
      for (var tj = 0; tj < tkeys.length; tj++) {
        if (tt.indexOf(tkeys[tj]) !== -1) { tt = tt.split(tkeys[tj]).join(tmap[tkeys[tj]]); changedT = true; }
      }
      if (changedT) { document.title = tt; }
    }
    // M72：屬性翻譯（title/aria-label/placeholder）。
    var aEls = document.querySelectorAll('[data-i18n-title],[data-i18n-aria],[data-i18n-placeholder]');
    for (var ai = 0; ai < aEls.length; ai++) {
      var el = aEls[ai];
      if (el.hasAttribute('data-i18n-title')) { var kt = el.getAttribute('data-i18n-title'); if (d[kt]) { el.setAttribute('title', d[kt]); } }
      if (el.hasAttribute('data-i18n-aria')) { var ka = el.getAttribute('data-i18n-aria'); if (d[ka]) { el.setAttribute('aria-label', d[ka]); } }
      if (el.hasAttribute('data-i18n-placeholder')) { var kp = el.getAttribute('data-i18n-placeholder'); if (d[kp]) { el.setAttribute('placeholder', d[kp]); } }
    }
    // 全域文字節點替換（英文模式：中文→英文；中文模式：英文→中文）。
    if (lang === 'en') { replaceTextNodes(zh2en, zhKeys); }
    else { replaceTextNodes(en2zh, enKeys); }
    var btns = document.querySelectorAll('[data-i18n-lang]');
    for (var j = 0; j < btns.length; j++) {
      btns[j].textContent = lang === 'en' ? '中文' : 'EN';
      btns[j].setAttribute('aria-label', lang === 'en' ? 'Switch to Chinese' : 'Switch to English');
    }
    document.documentElement.lang = lang === 'en' ? 'en' : 'zh-Hant';
  }
  function toggle() {
    var next = cur() === 'en' ? 'zh' : 'en';
    try { localStorage.setItem(KEY, next); } catch (e) {}
    apply();
    try { window.dispatchEvent(new CustomEvent('tac-lang-changed', { detail: { lang: next } })); } catch (e) {}
  }
  // TAC_T(key)：JS 動態字串翻譯（未命中回傳 key 本身）。
  window.TAC_T = function (key) {
    var lang = cur();
    var d = dict[lang] || dict.en;
    return d[key] || key;
  };
  // TAC_TE(s)：把「中文短語字串」直接翻成當前語言（查 zh2en 雙向字典）。
  // 用於後端 API 返回的 message／前端 alert/confirm/prompt 字面量；英文模式未命中時原樣返回。
  window.TAC_TE = function (s) {
    if (!s || typeof s !== 'string') { return s; }
    var lang = cur();
    if (lang === 'en') { return zh2en[s] || s; }
    return en2zh[s] || s;
  };
  window.TAC_I18N = { cur: cur, apply: apply, toggle: toggle };
  // v163：頁面級詞條擴充——各新頁（dapp/agent 等）於自身 script 呼叫
  // window.TAC_LOAD_I18N({zh:{key:'中文'},en:{key:'English'}}) 注入詞條後立即重譯，
  // 免於直接修改 i18n.js（平行支線互不衝突）。可重複呼叫合併。
  // 若頁面 script 早於 i18n.js 執行，可先把資料放到 window.TAC_EXTRA_I18N，初始化時會自動合併。
  window.TAC_LOAD_I18N = function (extra) {
    try {
      if (extra && extra.zh) { for (var kz in extra.zh) { dict.zh[kz] = extra.zh[kz]; } }
      if (extra && extra.en) { for (var ke in extra.en) { dict.en[ke] = extra.en[ke]; } }
    } catch (e) { /* 忽略壞詞條 */ }
    apply();
  };
  try {
    if (window.TAC_EXTRA_I18N) {
      var ex = window.TAC_EXTRA_I18N;
      if (ex.zh) { for (var kx in ex.zh) { dict.zh[kx] = ex.zh[kx]; } }
      if (ex.en) { for (var ky in ex.en) { dict.en[ky] = ex.en[ky]; } }
    }
  } catch (e) { /* 忽略 */ }
  document.addEventListener('DOMContentLoaded', apply);
  apply();
  // 自動套用：JS 動態渲染（innerHTML/append）後自動翻譯，無需各頁手動呼叫。
  try {
    var mo = new MutationObserver(function () {
      clearTimeout(window.__tac_i18n_t);
      window.__tac_i18n_t = setTimeout(function () {
        var lang = cur();
        if (lang === 'en') { replaceTextNodes(zh2en, zhKeys); }
        else { replaceTextNodes(en2zh, enKeys); }
      }, 120);
    });
    mo.observe(document.body, { childList: true, subtree: true });
  } catch (e) { /* 舊瀏覽器忽略 */ }

  // v165 修復：以下詞條 v164 誤植在 IIFE 外（此時 dict 已脫離作用域 → 每頁 ReferenceError: dict is not defined，
  // 導致 tiusd/nh/zk/vrf 全部 key 未註冊、後續敘述中斷）。搬回 IIFE 內部，dict 仍在作用域、TAC_T 可查。
  dict.zh['tiusd.hookPending']='Webhook 待投遞 ';
  dict.en['tiusd.hookPending']='Webhook pending ';

  dict.zh['nh.priceTitle']='資產行情（Oracle）';
  dict.en['nh.priceTitle']='Asset Prices (Oracle)';
  dict.zh['nh.listed']='已上市';
  dict.en['nh.listed']='Listed';
  dict.zh['nh.unlisted']='未上市（0.330 兜底）';
  dict.en['nh.unlisted']='Unlisted (0.330 floor)';
  dict.zh['tiusd.hookFailed']='重試佇列 ';
  dict.en['tiusd.hookFailed']='retry queue ';
  dict.zh['zk.title']='ZK／VRF 工具台'; dict.zh['zk.sub']='零知識原語 — 狀態批次證明 · 承諾 · Nullifier · 可驗證隨機數';
  dict.zh['zk.batch']='ZK-Rollup 批次證明'; dict.zh['zk.old']='舊狀態根'; dict.zh['zk.new']='新狀態根';
  dict.zh['zk.prove']='產生證明'; dict.zh['zk.verify']='驗證';
  dict.zh['zk.commitTitle']='隱私承諾與 Nullifier'; dict.zh['zk.commitSub']='隱藏性＋綁定性 · 防雙花';
  dict.zh['zk.msg']='金額/訊息'; dict.zh['zk.commit']='產生承諾';
  dict.zh['vrf.title']='可驗證隨機函數（ECVRF）'; dict.zh['vrf.sub']='公平抽籤 · 節點選舉 · 白名單';
  dict.zh['vrf.keygen']='產生密鑰'; dict.zh['vrf.msg']='訊息'; dict.zh['vrf.prove']='產生證明'; dict.zh['vrf.verify']='驗證';
  dict.en['zk.title']='ZK &amp; VRF Toolkit'; dict.en['zk.sub']='Zero-Knowledge primitives — state batch proof · commitment · nullifier · verifiable randomness';
  dict.en['zk.batch']='ZK-Rollup Batch Proof'; dict.en['zk.old']='old state root'; dict.en['zk.new']='new state root';
  dict.en['zk.prove']='Prove'; dict.en['zk.verify']='Verify';
  dict.en['zk.commitTitle']='Privacy Commitment &amp; Nullifier'; dict.en['zk.commitSub']='hiding + binding · double-spend guard';
  dict.en['zk.msg']='amount/message'; dict.en['zk.commit']='Commit';
  dict.en['vrf.title']='Verifiable Random Function (ECVRF)'; dict.en['vrf.sub']='fair lottery · node election · whitelist';
  dict.en['vrf.keygen']='Generate Key'; dict.en['vrf.msg']='message'; dict.en['vrf.prove']='Prove'; dict.en['vrf.verify']='Verify';
})();
