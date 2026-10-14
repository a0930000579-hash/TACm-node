// TAC Ai 智能鏈官方 JavaScript SDK（瀏覽器/Node 通用）。
// 與 Go 端（internal/crypto）簽章格式互通：TxSighash 白名單、
// Canonical 排序 JSON、DoubleSHA256、SHA256 後 RFC6979 ECDSA、DER 簽名。
import { sign as nobleSign, getPublicKey, utils as secpUtils, etc } from '@noble/secp256k1';
import { sha256 } from '@noble/hashes/sha256';
import { ripemd160 } from '@noble/hashes/ripemd160';
import { hmac } from '@noble/hashes/hmac';

// noble 同步簽章需要 HMAC-SHA256（RFC6979 內部隨機）。
etc.hmacSha256Sync = (k, ...m) => hmac(sha256, k, etc.concatBytes(...m));

const BASE58 = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';

function toBytes(hexOrBytes) {
  if (typeof hexOrBytes === 'string') {
    const clean = hexOrBytes.startsWith('0x') ? hexOrBytes.slice(2) : hexOrBytes;
    if (!/^[0-9a-fA-F]*$/.test(clean) || clean.length % 2 !== 0) {
      throw new Error('tacjs: 非法 hex');
    }
    return Uint8Array.from(clean.match(/.{2}/g) || [], (b) => parseInt(b, 16));
  }
  return Uint8Array.from(hexOrBytes);
}

function toHex(bytes) {
  return Array.from(bytes).map((b) => b.toString(16).padStart(2, '0')).join('');
}

function sha256d(data) {
  return sha256(sha256(data));
}

// Base58Check 編碼（前綴 1 字節 + 內容 + double-sha256 前 4 字節）。
function base58checkEncode(version, payload) {
  const buf = new Uint8Array(1 + payload.length + 4);
  buf.set([version], 0);
  buf.set(payload, 1);
  const checksum = sha256d(buf.subarray(0, 1 + payload.length));
  buf.set(checksum.subarray(0, 4), 1 + payload.length);
  let n = 0n;
  for (const b of buf) n = (n << 8n) + BigInt(b);
  let out = '';
  while (n > 0n) {
    out = BASE58[n % 58n] + out;
    n /= 58n;
  }
  for (const b of buf) if (b === 0) out = '1' + out; else break;
  return out;
}

// Hash160 = ripemd160(sha256(data))。
export function hash160(data) {
  return ripemd160(sha256(toBytes(data)));
}

// 壓縮公鑰 → tx0 地址（與 Go PubKeyToAddress 同構）。
export function addressFromPubkey(compressedPubHex) {
  const pub = toBytes(compressedPubHex);
  if (pub.length !== 33) throw new Error('tacjs: 公鑰須為 33 字節壓縮格式');
  return 'tx0' + base58checkEncode(0, hash160(pub));
}

// 生成新密鑰對。
export function generateKey() {
  const priv = secpUtils.randomPrivateKey();
  const pub = getPublicKey(priv, true); // 壓縮
  const privHex = toHex(priv);
  const pubHex = toHex(pub);
  return { privateKeyHex: privHex, publicKeyHex: pubHex, address: addressFromPubkey(pubHex) };
}

// 從 32 字節 hex 私鑰導入。
export function keyFromPrivateKeyHex(hexKey) {
  const priv = toBytes(hexKey);
  if (priv.length !== 32) throw new Error('tacjs: 私鑰須為 32 字節');
  const pub = getPublicKey(priv, true);
  const pubHex = toHex(pub);
  return { privateKeyHex: toHex(priv), publicKeyHex: pubHex, address: addressFromPubkey(pubHex) };
}

// 規範排序 JSON（與 Go crypto.Canonical 同構：keys 排序、字串 JSON 規則）。
function canonicalStringify(v) {
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'string') return JSON.stringify(v);
  if (typeof v === 'number') {
    if (!Number.isFinite(v)) throw new Error('tacjs: 非法數字');
    return Object.is(v, -0) ? '0' : String(v);
  }
  if (typeof v === 'boolean') return v ? 'true' : 'false';
  if (Array.isArray(v)) return '[' + v.map(canonicalStringify).join(',') + ']';
  if (typeof v === 'object') {
    const keys = Object.keys(v).sort();
    return '{' + keys.map((k) => JSON.stringify(k) + ':' + canonicalStringify(v[k])).join(',') + '}';
  }
  throw new Error('tacjs: 無法規範序列化 ' + typeof v);
}

// 交易簽章白名單（與 Go txSignFields 一致）。
const TX_SIGN_FIELDS = ['from', 'to', 'amount', 'fee', 'memo', 'ts', 'nonce', 'token'];

// TxSighash = DoubleSHA256(Canonical(白名單字段))。
export function txSighash(tx) {
  const filtered = {};
  for (const k of TX_SIGN_FIELDS) {
    if (tx[k] !== undefined) filtered[k] = tx[k];
    else if (k === 'memo' || k === 'token') filtered[k] = '';
  }
  return sha256d(new TextEncoder().encode(canonicalStringify(filtered)));
}

// bigint r/s → 標準 DER（30 len 02 rlen r 02 slen s，去前導零、最高位補 0x00）。
function derEncode(r, s) {
  const rBytes = numToBytes(r);
  const sBytes = numToBytes(s);
  const enc = (b) => {
    const body = (b[0] & 0x80) ? new Uint8Array([0, ...b]) : b;
    return new Uint8Array([0x02, body.length, ...body]);
  };
  const er = enc(rBytes);
  const es = enc(sBytes);
  const der = new Uint8Array([0x30, er.length + es.length, ...er, ...es]);
  return der;
}

function numToBytes(n) {
  let hex = n.toString(16);
  if (hex.length % 2) hex = '0' + hex;
  let b = toBytes(hex);
  while (b.length > 1 && b[0] === 0) b = b.subarray(1); // 去前導零
  return b;
}

// 簽署交易：TxSighash → SHA256 → RFC6979 ECDSA → DER hex（與 Go SignTransaction 同構）。
export function signTransaction(tx, privateKeyHex) {
  const priv = toBytes(privateKeyHex);
  const msgHash = sha256(txSighash(tx));
  const sig = nobleSign(msgHash, priv);
  return toHex(derEncode(sig.r, sig.s));
}

// ---- 節點 RPC 客戶端（fetch） ----

export class Client {
  constructor(baseURL) {
    this.base = String(baseURL).replace(/\/+$/, '');
  }

  async request(method, path, body) {
    const opts = { method, headers: {} };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    const resp = await fetch(this.base + path, opts);
    const text = await resp.text();
    if (!resp.ok) throw new Error(`tacjs: 節點錯誤 ${resp.status}: ${text}`);
    return text ? JSON.parse(text) : null;
  }

  async status() {
    return this.request('GET', '/status');
  }

  async account(address) {
    return this.request('GET', `/account/${encodeURIComponent(address)}`);
  }

  async balance(address) {
    const acc = await this.account(address);
    return acc.balance || '0';
  }

  async transaction(hash) {
    return this.request('GET', `/tx/${encodeURIComponent(hash)}`);
  }

  // 提交已簽署交易（需含 from/to/amount/fee/nonce/ts/signature/pubkey）。
  async submit(tx) {
    const res = await this.request('POST', '/tx/submit', tx);
    if (!res.ok || !res.tx_hash) throw new Error('tacjs: 節點未確認交易');
    return res.tx_hash;
  }

  // 一步轉帳：查 nonce → 建構 → 簽署 → 提交。
  async transfer(key, to, amount, fee, memo = '') {
    const acc = await this.account(key.address);
    const tx = {
      from: key.address, to,
      amount, fee,
      nonce: acc.nonce || 0,
      ts: Math.floor(Date.now() / 1000),
      memo,
      pubkey: key.publicKeyHex,
    };
    tx.signature = signTransaction(tx, key.privateKeyHex);
    return this.submit(tx);
  }
}
