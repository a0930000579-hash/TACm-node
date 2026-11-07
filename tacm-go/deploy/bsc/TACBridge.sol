// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

// =====================================================================
// TAC Bridge — BSC 側多資產映射合約（零依賴，Remix 可直接編譯）
// M149s：TACm/TiUSD/USDT 多資產互通，非單一資產照搬。
//
// 部署流程（BSC 測試網 ChainID 97）：
//   1) 部署 TACBridge（constructor 無參數）→ 記 BRIDGE_ADDR
//   2) 呼叫 deployMapped("tacm", "TACM on BSC")  → 記 TACM_TOKEN
//   3) 呼叫 deployMapped("tiusd","TiUSD on BSC") → 記 TIUSD_TOKEN
//   4) 呼叫 deployMapped("usdt", "USDT on BSC")  → 記 USDT_TOKEN
//   5) 節點設定（env / flag）：
//        TAC_BSC_ASSETS=[{"token":"tacm","bsc_token":"<TACM_TOKEN>","bsc_lock_proxy":"<BRIDGE_ADDR>","min_amount":0.001},
//                        {"token":"tiusd","bsc_token":"<TIUSD_TOKEN>","bsc_lock_proxy":"<BRIDGE_ADDR>","min_amount":1},
//                        {"token":"usdt","bsc_token":"<USDT_TOKEN>","bsc_lock_proxy":"<BRIDGE_ADDR>","min_amount":1}]
// 事件簽章與 Go 端一致：Deposit(address,address,address,uint256,uint256)（前三 indexed）。
// =====================================================================

// MappedToken：零依賴 ERC-20，mint/burn 僅限 bridge（TACBridge）。
contract MappedToken {
    string public name;
    string public symbol;
    uint8 public constant decimals = 18;
    uint256 public totalSupply;
    address public bridge;

    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    event Transfer(address indexed from, address indexed to, uint256 value);
    event Approval(address indexed owner, address indexed spender, uint256 value);

    modifier onlyBridge() { require(msg.sender == bridge, "only bridge"); _; }

    constructor(string memory _name, string memory _symbol, address _bridge) {
        name = _name;
        symbol = _symbol;
        bridge = _bridge;
    }

    function mint(address to, uint256 amount) external onlyBridge {
        require(to != address(0), "mint to zero");
        totalSupply += amount;
        balanceOf[to] += amount;
        emit Transfer(address(0), to, amount);
    }

    function burn(address from, uint256 amount) external onlyBridge {
        require(balanceOf[from] >= amount, "burn exceeds");
        balanceOf[from] -= amount;
        totalSupply -= amount;
        emit Transfer(from, address(0), amount);
    }

    function setBridge(address b) external onlyBridge { bridge = b; }

    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount;
        emit Approval(msg.sender, spender, amount);
        return true;
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        _transfer(msg.sender, to, amount);
        return true;
    }

    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        uint256 allowed = allowance[from][msg.sender];
        if (allowed != type(uint256).max) {
            require(allowed >= amount, "allowance");
            allowance[from][msg.sender] = allowed - amount;
        }
        _transfer(from, to, amount);
        return true;
    }

    function _transfer(address from, address to, uint256 amount) internal {
        require(balanceOf[from] >= amount, "insufficient");
        require(to != address(0), "to zero");
        balanceOf[from] -= amount;
        balanceOf[to] += amount;
        emit Transfer(from, to, amount);
    }
}

// TACBridge：多資產映射 registry + 鎖定/解鎖入口（單一 LockProxy 管理多代幣）。
contract TACBridge {
    address public owner;
    string[] public symbols;
    mapping(string => address) public mappedTokens;   // symbol → MappedToken
    mapping(string => bool) public registered;
    uint256 public nonceCounter;

    event Deposit(address indexed token, address indexed from, address indexed to, uint256 amount, uint256 nonce);
    event Withdraw(address indexed token, address indexed to, uint256 amount, uint256 nonce);
    event Mapped(string indexed symbol, address token);

    constructor() { owner = msg.sender; }

    modifier onlyOwner() { require(msg.sender == owner, "only owner"); _; }

    // 部署新映射代幣（每次呼叫部署一個 MappedToken）。
    function deployMapped(string calldata symbol, string calldata _name) external onlyOwner returns (address) {
        require(!registered[symbol], "already mapped");
        MappedToken tk = new MappedToken(_name, symbol, address(this));
        mappedTokens[symbol] = address(tk);
        registered[symbol] = true;
        symbols.push(symbol);
        emit Mapped(symbol, address(tk));
        return address(tk);
    }

    // 用戶存入：轉入映射代幣並燒毀（跨鏈前鎖定）。
    function deposit(string calldata symbol, address to, uint256 amount) external {
        address tk = mappedTokens[symbol];
        require(tk != address(0), "unknown symbol");
        require(MappedToken(tk).transferFrom(msg.sender, address(this), amount), "tf");
        MappedToken(tk).burn(address(this), amount);
        nonceCounter++;
        emit Deposit(tk, msg.sender, to, amount, nonceCounter);
    }

    // owner（Relayer）呼叫：為 to 取回（跨鏈反向退款用）。
    function withdraw(string calldata symbol, address to, uint256 amount, uint256 nonce) external onlyOwner {
        address tk = mappedTokens[symbol];
        require(tk != address(0), "unknown symbol");
        MappedToken(tk).mint(to, amount);
        emit Withdraw(tk, to, amount, nonce);
    }

    function tokenOf(string calldata symbol) external view returns (address) {
        return mappedTokens[symbol];
    }

    function symbolCount() external view returns (uint256) {
        return symbols.length;
    }
}
