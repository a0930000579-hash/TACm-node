#!/usr/bin/env node
// TAC Bridge BSC 部署腳本（M149s：一鍵「拿地址」補齊跨鏈閉環）。
// 用法：
//   npm install ethers@6
//   node deploy.js 0x<你的私鑰> [bsc|bsctest]
// 輸出：BRIDGE_ADDR＋三個映射代幣地址＋可直接貼入節點 env 的 TAC_BSC_ASSETS。
// 注意：私鑰僅在本地使用；測試網需少量 tBNB（faucet: https://www.bnbchain.org/en/testnet-faucet）。
const { ethers } = require("ethers");
const fs = require("fs");
const path = require("path");

const SOL = fs.readFileSync(path.join(__dirname, "TACBridge.sol"), "utf8");

const NETWORKS = {
  bsc: { chainId: 56, rpc: "https://bsc-dataseed.bnbchain.org" },
  bsctest: { chainId: 97, rpc: "https://data-seed-prebsc-1-s1.bnbchain.org:8545" },
};

async function main() {
  const pk = process.argv[2];
  const net = NETWORKS[process.argv[3] || "bsctest"];
  if (!pk) throw new Error("用法: node deploy.js 0x<私鑰> [bsc|bsctest]");

  const provider = new ethers.JsonRpcProvider(net.rpc);
  const signer = new ethers.Wallet(pk, provider);
  console.log("部署者:", signer.address, "| ChainID:", net.chainId);

  // 0) 編譯（不含 OpenZeppelin；solc 由 ethers 依賴提供）。
  const { Interface } = ethers;
  const { abi, bytecode } = compile(SOL);

  // 1) 部署 TACBridge
  const factory = new ethers.ContractFactory(abi, bytecode, signer);
  const bridge = await factory.deploy();
  await bridge.waitForDeployment();
  const bridgeAddr = await bridge.getAddress();
  console.log("TACBridge:", bridgeAddr);

  // 2) deployMapped 三資產
  const mapped = {};
  for (const [sym, name] of [["tacm", "TACM on BSC"], ["tiusd", "TiUSD on BSC"], ["usdt", "USDT on BSC"]]) {
    const tx = await bridge.deployMapped(sym, name);
    await tx.wait();
    const addr = await bridge.tokenOf(sym);
    mapped[sym] = addr;
    console.log(`deployMapped(${sym}) →`, addr);
  }

  // 3) 輸出 env 模板
  const assets = Object.entries(mapped).map(([sym, addr]) => ({
    token: sym,
    bsc_token: addr,
    bsc_lock_proxy: bridgeAddr,
    min_amount: sym === "tacm" ? 0.001 : 1,
  }));
  console.log("\n=== 貼入節點環境變數（部署後免改碼） ===\n");
  console.log("TAC_BSC_ASSETS=" + JSON.stringify(assets));
}

// 極簡 solc 包裝：此專案無依賴可編譯；若無 solc-js 會提示安裝。
function compile(src) {
  try {
    const solc = require("solc");
    const input = {
      language: "Solidity",
      sources: { "TACBridge.sol": { content: src } },
      settings: { outputSelection: { "*": { "*": ["abi", "evm.bytecode"] } } },
    };
    const out = JSON.parse(solc.compile(JSON.stringify(input)));
    if (out.errors) {
      const fatal = out.errors.filter((e) => e.severity === "error");
      if (fatal.length) throw new Error(fatal.map((e) => e.formattedMessage).join("\n"));
    }
    const c = out.contracts["TACBridge.sol"]["TACBridge"];
    return { abi: c.abi, bytecode: "0x" + c.evm.bytecode.object };
  } catch (e) {
    throw new Error("需要 solc：npm install solc ethers@6（" + e.message + "）");
  }
}

main().catch((e) => { console.error("失敗:", e.message); process.exit(1); });
