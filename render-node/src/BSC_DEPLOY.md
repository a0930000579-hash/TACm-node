# TAC ↔ BSC 真實跨鏈橋部署教學（M50）

本文件說明如何把 TAC Ai 智能鏈與 BNB Smart Chain（BSC）對接為**真實跨鏈橋**：
TAC 側鎖定 TACM → BSC 側由 Relayer 自動 mint 映射代幣；BSC 側用戶在
LockProxy `deposit` 燒毀映射代幣 → TAC 側自動建立解鎖記錄，由守衛網絡完成鏈上解鎖。

> 本版完成的是**鏈上簽署／ABI／事件解析／雙向 Relayer** 的完整工程層，
> 合約與 RPC 均為標準 EVM 規格，可對接 BSC 測試網與主網。

---

## 1. 架構總覽

```
        TAC Ai 智能鏈（自有鏈，Go）                BNB Smart Chain（BSC）
 ┌──────────────────────────────┐         ┌────────────────────────────────┐
 │ 節點（-bridge -bsc-relay）    │         │ MintBurn 代幣（TACM 映射）      │
 │  ├ 橋狀態機 pending→locked→   │         │  ├ mint(address,uint256)       │
 │  │   minted→confirmed         │         │  │   （僅 Relayer 可調）        │
 │  └ Relayer（雙向 goroutine）  │◄───BSC──►│  └ burn(uint256)               │
 │      ├ TAC→BSC：掃 locked →   │ JSON-RPC │ LockProxy 合約                │
 │      │  簽署 mint 交易上鏈    │         │  ├ deposit(address to,uint256)│
 │      └ BSC→TAC：掃 Deposit    │◄───BSC──►│  │   （燒幣＋廣播 Deposit）     │
 │          事件 → 建解鎖記錄    │  events  │  └ withdraw(uint256 nonce)    │
 └──────────────────────────────┘         └────────────────────────────────┘
```

- **TAC→BSC（資產出金）**：用戶呼叫 `POST /bridge/lock`（source=tacm, target=bsc）
  → `POST /bridge/confirm-lock` 提交源鏈鎖定證明 → Relayer 掃到 `locked` 後
  用 BSC 側私鑰簽署 `mint(to, amountWei)` 上鏈 → `MintOnTarget` 推進狀態為 minted。
- **BSC→TAC（資產入金）**：用戶在 BSC 呼叫 `LockProxy.deposit(to, amount)`
  （燒毀映射代幣）→ Relayer 掃到 `Deposit` 事件 → 在 TAC 橋建立
  `pending→burning` 解鎖記錄 → 守衛網絡/既有橋流程完成 TAC 鏈上解鎖。

---

## 2. BSC 側合約（Solidity 摘要）

### 2.1 MintBurn 代幣（TACM 在 BSC 的映射）

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/token/ERC20/ERC20.sol";

contract TACMBSC is ERC20 {
    address public bridge; // 僅 bridge 可 mint/burn

    constructor() ERC20("TACM on BSC", "TACM") {
        bridge = msg.sender;
    }

    function mint(address to, uint256 amount) external {
        require(msg.sender == bridge, "only bridge");
        _mint(to, amount);
    }

    function burn(uint256 amount) external {
        require(msg.sender == bridge, "only bridge");
        _burn(msg.sender, amount);
    }

    function setBridge(address b) external {
        require(msg.sender == bridge, "only bridge");
        bridge = b;
    }
}
```

### 2.2 LockProxy（用戶存入／取回入口）

```solidity
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import "@openzeppelin/contracts/token/ERC20/IERC20.sol";

contract TACLockProxy {
    IERC20 public token;
    address public bridge;      // 管理員（可呼叫 withdraw 為用戶取回）
    uint256 public nonceCounter;

    event Deposit(address indexed token, address indexed from,
                  address indexed to, uint256 amount, uint256 nonce);
    event Withdraw(address indexed token, address indexed to,
                   uint256 amount, uint256 nonce);

    constructor(address _token) {
        token = IERC20(_token);
        bridge = msg.sender;
    }

    // 用戶存入：代幣轉入本合約並銷毀（跨鏈前鎖定）。
    function deposit(address to, uint256 amount) external {
        require(token.transferFrom(msg.sender, address(this), amount), "tf");
        _burnInternal(amount);
        nonceCounter++;
        emit Deposit(address(token), msg.sender, to, amount, nonceCounter);
    }

    // 由 bridge 管理員呼叫：為 to 取回（跨鏈反向後退款）。
    function withdraw(address to, uint256 amount, uint256 nonce) external {
        require(msg.sender == bridge, "only bridge");
        _mintInternal(to, amount);
        emit Withdraw(address(token), to, amount, nonce);
    }

    function _burnInternal(uint256 amount) private {
        // 對接 MintBurn 代幣的 burn(address,uint256) 授權接口；
        // 若代幣僅支持 msg.sender burn，改用 burnFrom 授權模式。
        (bool ok, ) = address(token).call(
            abi.encodeWithSignature("burn(uint256)", amount));
        require(ok, "burn failed");
    }

    function _mintInternal(address to, uint256 amount) private {
        (bool ok, ) = address(token).call(
            abi.encodeWithSignature("mint(address,uint256)", to, amount));
        require(ok, "mint failed");
    }
}
```

> 事件簽章必須與 Go 端一致：
> `Deposit(address,address,address,uint256,uint256)`（前三項 indexed）。
> 部署後記錄：`TOKEN_ADDR`、`LOCKPROXY_ADDR`。

### 2.3 部署步驟（任選其一）

**A. Remix（最快驗證）**
1. 開啟 https://remix.ethereum.org → 新建 `TACMBSC.sol`、`TACLockProxy.sol`。
2. 環境選 **Injected Provider – MetaMask**，網路切 **BNB Smart Chain Testnet**
   （RPC: `https://data-seed-prebsc-1-s1.bnbchain.org:8545`，ChainID **97**）。
3. 依序部署 `TACMBSC` → 記 `TOKEN_ADDR` → 部署 `TACLockProxy(TOKEN_ADDR)` → 記 `LOCKPROXY_ADDR`。
4. 在 `TACMBSC.setBridge(LOCKPROXY_ADDR)` 把 bridge 設為 LockProxy。
5. 用測試網 BNB 付 gas（免費水龍頭：https://testnet.bnbchain.org/faucet-smart）。

**B. Hardhat（正式）**
```bash
npx hardhat init
# contracts/TACMBSC.sol、contracts/TACLockProxy.sol 如上
npx hardhat run scripts/deploy.js --network bscTestnet
```

---

## 3. 啟動 TAC 節點並啟用 BSC Relayer

### 3.1 本機（Linux/macOS/WSL）

```bash
cd tacm-go
GOTOOLCHAIN=local go build -o tacweb ./cmd/web

./tacweb \
  -web-port 8080 -rpc-port 8080 \
  -data-dir ./tac_data -block-time 1 -difficulty 1 \
  -bridge \
  -bsc-relay \
  -bsc-rpc https://data-seed-prebsc-1-s1.bnbchain.org:8545 \
  -bsc-pk <你的 BSC 私鑰 hex，32 字節> \
  -bsc-chain-id 97 \
  -bsc-token 0x<部署的 TACMBSC 地址> \
  -bsc-lock-proxy 0x<部署的 TACLockProxy 地址> \
  -bsc-poll-ms 5000
```

> `-bsc-pk` 對應的地址需持有少量 BNB（測試網免費）以支付 mint 交易 gas。
> 查 Relayer 地址：`curl http://localhost:8080/bridge/bsc/status`（`signer_addr` 欄位）。

### 3.2 Render（雲端，與既有部署一致）

在 Render 服務 **Environment** 增加：

| 變數 | 值 |
|---|---|
| `BSC_RELAY` | `1` |
| `BSC_RPC` | `https://data-seed-prebsc-1-s1.bnbchain.org:8545` |
| `BSC_PK` | 你的 BSC 私鑰（用 Secret 存放） |
| `BSC_CHAIN_ID` | `97` |
| `BSC_TOKEN` | `0x<TOKEN_ADDR>` |
| `BSC_LOCK_PROXY` | `0x<LOCKPROXY_ADDR>` |
| `BSC_POLL_MS` | `5000` |

Start Command：

```bash
cd tacm-go && ./tacweb -web-port $PORT -rpc-port $PORT -data-dir ./data -block-time 1 -difficulty 1 -bridge -bsc-relay -bsc-rpc "$BSC_RPC" -bsc-pk "$BSC_PK" -bsc-chain-id $BSC_CHAIN_ID -bsc-token "$BSC_TOKEN" -bsc-lock-proxy "$BSC_LOCK_PROXY" -bsc-poll-ms $BSC_POLL_MS
```

---

## 4. 驗收

```bash
# 1. Relayer 狀態（enabled=true、signer_addr 有值）
curl http://localhost:8080/bridge/bsc/status

# 2. TAC→BSC：建單＋鎖定確認（target_chain=bsc）
curl -X POST http://localhost:8080/bridge/lock -H 'Content-Type: application/json' -d '{
  "source_chain":"tacm","target_chain":"bsc",
  "source_address":"<tx0地址>","target_address":"<tx0地址>",
  "amount":10,"token":"TACM"}'
# → 取 bridge_tx_id
curl -X POST http://localhost:8080/bridge/confirm-lock -H 'Content-Type: application/json' -d '{
  "bridge_tx_id":"<id>","tx_hash":"0x<源鏈鎖定證明>"}'
# → 數秒後 BSC 鏈上出現 mint 交易；再查
curl -X POST http://localhost:8080/bridge/status -H 'Content-Type: application/json' -d '{"bridge_tx_id":"<id>"}'
# → status=minted / confirmed

# 3. BSC→TAC：在 BSC 呼叫 LockProxy.deposit(to, amount)
# → Relayer 掃到 Deposit 事件後自動建立解鎖記錄（status=pending→burning）
# → 守衛網絡完成 TAC 鏈上解鎖
```

日誌確認：

```
[bsc-relay] TAC→BSC mint 完成 id=... bsc_tx=0x...
[bsc-relay] BSC→TAC 解鎖單已建立 id=... bsc_tx=0x...
```

---

## 5. 安全備註

- `-bsc-pk` 屬機密：Render 用 Secret、本機勿寫入 shell history／提交 git。
- 主網部署請改 `-bsc-chain-id 56`，並將 `Confirmations` 設 ≥12（掃描確認數）。
- mint/burn 權限僅限 LockProxy，避免任意鑄造；代幣總量受 TAC 側供應上限制約。
- 本版 Relayer 的 BSC→TAC 自動化建單＋BSC 側真實 mint 已完整；
  TAC 側鏈上解鎖沿用守衛網絡既有流程（-bridge-guardians）。
