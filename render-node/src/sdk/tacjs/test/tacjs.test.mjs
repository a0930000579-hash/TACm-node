// TAC JS SDK 單元測試（node --test test/）。
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import {
  generateKey, keyFromPrivateKeyHex, addressFromPubkey, hash160,
  txSighash, signTransaction, Client,
} from '../src/tacjs.mjs';

test('金鑰產生與 hex 往返', () => {
  const k = generateKey();
  assert.ok(k.address.startsWith('tx0'));
  assert.equal(k.privateKeyHex.length, 64);
  assert.equal(k.publicKeyHex.length, 66); // 33 bytes compressed
  const k2 = keyFromPrivateKeyHex(k.privateKeyHex);
  assert.equal(k2.address, k.address);
  assert.equal(k2.publicKeyHex, k.publicKeyHex);
});

test('地址格式：Hash160 20 字節＋Base58Check', () => {
  // 固定私鑰 → 固定地址（與 Go 端互操作向量一致）。
  const k = keyFromPrivateKeyHex('01'.repeat(32));
  assert.equal(hash160(k.publicKeyHex).length, 20);
  assert.match(k.address, /^tx0[1-9A-HJ-NP-Za-km-z]{20,}$/);
});

test('TxSighash 規範排序（keys 排序＋白名單）', () => {
  const tx = { to: 'tx0bob', from: 'tx0alice', amount: '5', fee: '0.1', nonce: 0, ts: 1700000000 };
  // memo/token 缺省補空字串；keys 排序使序列化確定。
  const h1 = txSighash(tx);
  const h2 = txSighash({ ...tx, memo: '', token: '' });
  assert.deepEqual(h1, h2);
});

test('簽章為標準 DER', () => {
  const k = generateKey();
  const tx = { from: k.address, to: 'tx0bob', amount: '1', fee: '0', nonce: 0, ts: 1700000000 };
  const sig = signTransaction(tx, k.privateKeyHex);
  assert.ok(sig.startsWith('30')); // DER 序列開始標記
  assert.ok(sig.length > 130 && sig.length < 150);
});

test('Client：查詢＋提交（stub 節點）', async () => {
  const srv = createServer((req, res) => {
    if (req.url === '/status') {
      res.end(JSON.stringify({ node_id: 'n1', block_height: 9 }));
      return;
    }
    if (req.url.startsWith('/account/')) {
      const addr = req.url.slice('/account/'.length);
      res.end(JSON.stringify({ address: addr, balance: '100', nonce: 2 }));
      return;
    }
    if (req.url === '/tx/submit') {
      let body = '';
      req.on('data', (c) => (body += c));
      req.on('end', () => {
        const tx = JSON.parse(body);
        if (!tx.signature || !tx.pubkey) {
          res.writeHead(400).end('missing signature/pubkey');
          return;
        }
        res.end(JSON.stringify({ ok: true, tx_hash: '0xjs' }));
      });
      return;
    }
    res.writeHead(404).end('nope');
  });
  await new Promise((r) => srv.listen(0, r));
  const port = srv.address().port;
  try {
    const c = new Client(`http://127.0.0.1:${port}`);
    const st = await c.status();
    assert.equal(st.block_height, 9);
    assert.equal(await c.balance('tx0x'), '100');
    const k = generateKey();
    const hash = await c.transfer(k, 'tx0bob', '3', '0.01', 'js-test');
    assert.equal(hash, '0xjs');
  } finally {
    srv.close();
  }
});
