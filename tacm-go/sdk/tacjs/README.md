# TAC JS SDK（tacjs）

TAC Ai 智能鏈官方 **JavaScript** SDK——瀏覽器與 Node 通用，與 Go SDK
（`sdk/tacclient`）簽章格式互通：**前端用 tacjs 簽署的交易，Go 節點可直接驗證入池**
（互操作已由 `sdk/tacclient/js_interop_test.go` 固定向量測試證明）。

## 安裝

```bash
cd sdk/tacjs
npm install          # 依賴 @noble/secp256k1 + @noble/hashes（純 JS）
npm test             # node --test 單元測試
```

## 用法

```js
import { generateKey, keyFromPrivateKeyHex, signTransaction, Client } from './src/tacjs.mjs';

// 1) 密鑰
const key = generateKey();
console.log(key.address);                 // tx0...
const restored = keyFromPrivateKeyHex(key.privateKeyHex);

// 2) 節點客戶端
const c = new Client('http://127.0.0.1:8080');
const st = await c.status();              // 節點狀態
const bal = await c.balance(key.address); // 地址餘額

// 3) 一步轉帳（查 nonce → 簽署 → 提交）
const hash = await c.transfer(key, 'tx0收款地址', '10', '0.1', 'memo');

// 4) 自組交易（含合約呼叫）
const tx = {
  from: key.address, to: 'tx0合約地址',
  amount: '0', fee: '0', nonce: 0,
  ts: Math.floor(Date.now() / 1000),
  memo: 'vm:call:5000000:<calldata-hex>',
  pubkey: key.publicKeyHex,
};
tx.signature = signTransaction(tx, key.privateKeyHex);
const h2 = await c.submit(tx);
```

## 與 Go 端的互通細節

| 項目 | Go（internal/crypto） | JS（tacjs） |
|---|---|---|
| 地址 | `PubKeyToAddress`（Hash160＋Base58Check，tx0 前綴） | `addressFromPubkey`（同構） |
| 簽名白名單 | `from/to/amount/fee/memo/ts/nonce/token` | `TX_SIGN_FIELDS`（一致） |
| 序列化 | `Canonical`（keys 排序 JSON） | `canonicalStringify`（同構） |
| 摘要 | DoubleSHA256 → SHA256 | `sha256d` → `sha256`（一致） |
| 簽章 | secp256k1 RFC6979 → DER hex | noble secp256k1 → DER hex（一致） |
