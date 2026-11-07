package node

// v130：鏈上拍賣 RPC（create/bid/settle/list/info/bids）。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// handleAuctionCreate POST /api/auction/create {seller, item, asset, start_price, end_height}
func (s *RPCServer) handleAuctionCreate(w http.ResponseWriter, r *http.Request) {
	am := s.node.Auction()
	if am == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "auction_disabled"})
		return
	}
	var req struct {
		Seller     string `json:"seller"`
		Item       string `json:"item"`
		Asset      string `json:"asset"`
		StartPrice string `json:"start_price"`
		EndHeight  int64  `json:"end_height"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	tip := s.node.DB().GetTipHeight()
	id, err := am.CreateAuction(req.Seller, req.Item, req.Asset, req.StartPrice, req.EndHeight, tip, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auction_id": id})
}

// handleAuctionBid POST /api/auction/bid {auction_id, bidder, amount}
func (s *RPCServer) handleAuctionBid(w http.ResponseWriter, r *http.Request) {
	am := s.node.Auction()
	if am == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "auction_disabled"})
		return
	}
	var req struct {
		AuctionID int64  `json:"auction_id"`
		Bidder    string `json:"bidder"`
		Amount    string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	tip := s.node.DB().GetTipHeight()
	if err := am.Bid(req.AuctionID, req.Bidder, req.Amount, tip, ""); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auction_id": req.AuctionID})
}

// handleAuctionSettle POST /api/auction/settle {auction_id}
func (s *RPCServer) handleAuctionSettle(w http.ResponseWriter, r *http.Request) {
	am := s.node.Auction()
	if am == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "auction_disabled"})
		return
	}
	var req struct {
		AuctionID int64 `json:"auction_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad_json"})
		return
	}
	tip := s.node.DB().GetTipHeight()
	auc, err := am.Settle(req.AuctionID, tip)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auction": auc})
}

// handleAuctionList GET /api/auction/list?status=open
func (s *RPCServer) handleAuctionList(w http.ResponseWriter, r *http.Request) {
	am := s.node.Auction()
	if am == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "auction_disabled"})
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != auctionOpen && status != auctionSold && status != auctionUnsold {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_status"})
		return
	}
	list, err := am.Auctions(status)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auctions": list})
}

// handleAuctionInfo GET /api/auction/info?id=N
func (s *RPCServer) handleAuctionInfo(w http.ResponseWriter, r *http.Request) {
	am := s.node.Auction()
	if am == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "auction_disabled"})
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid id"})
		return
	}
	auc, err := am.AuctionInfo(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if auc == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "auction_not_found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auction": auc})
}

// handleAuctionBids GET /api/auction/bids?auction_id=N
func (s *RPCServer) handleAuctionBids(w http.ResponseWriter, r *http.Request) {
	am := s.node.Auction()
	if am == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "auction_disabled"})
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("auction_id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid auction_id"})
		return
	}
	bids, err := am.Bids(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bids": bids})
}
