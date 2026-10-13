package node

// community_rpc.go — 內建社群 API（FB 風動態牆/按讚/留言/市集/廣告/轉交易所）。

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/community"
	"tacm/internal/exchange"
	"tacm/internal/wallet"
)

// communityAddr 要求成員地址參數（identity＝錢包地址）。
func communityAddr(r *http.Request) (string, error) {
	var body struct {
		Address string `json:"address"`
	}
	if r.Method == http.MethodGet {
		body.Address = r.URL.Query().Get("address")
	} else {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return "", fmt.Errorf("參數解析失敗")
		}
	}
	addr := strings.TrimSpace(body.Address)
	if addr == "" {
		return "", fmt.Errorf("缺少成員地址")
	}
	return addr, nil
}

// handleCommunityFeed GET /api/community/feed?limit=&offset=
func (s *RPCServer) handleCommunityFeed(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	// M73-B：追蹤中動態牆（scope=following 且帶 address）；順帶惰性結算獎勵。
	s.node.TryCommunityRewardSettle()
	if r.URL.Query().Get("scope") == "following" {
		addr := strings.TrimSpace(r.URL.Query().Get("address"))
		if addr == "" {
			writeErr(w, http.StatusBadRequest, "追蹤動態牆需要 address")
			return
		}
		items, err := cm.FollowingFeed(addr, limit, offset)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "scope": "following"})
		return
	}
	items, err := cm.Feed(limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// M97：FB/YT 式廣告插播素材（動態流 Sponsored / 影片流插播）。
	adPlace := strings.TrimSpace(r.URL.Query().Get("ad_placement"))
	if adPlace == "" {
		adPlace = "feed"
	}
	ads, aerr := cm.ActiveAds(adPlace, 6)
	if aerr != nil {
		ads = []community.Ad{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "ads": ads})
}

// handleCommunityPost POST /api/community/post {address,kind,content,image_url,price_tacm}
func (s *RPCServer) handleCommunityPost(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	var req struct {
		Address   string   `json:"address"`
		Kind      string   `json:"kind"`
		Content   string   `json:"content"`
		ImageURL  string   `json:"image_url"`
		VideoURL  string   `json:"video_url"`
		PriceTACM *float64 `json:"price_tacm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	if req.Kind == "" {
		req.Kind = "post"
	}
	id, err := cm.CreatePost(req.Address, req.Kind, req.Content, req.ImageURL, req.VideoURL, req.PriceTACM)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// M73-B：發文累積互動積分（鏈上獎勵結算用）。
	s.node.addRewardPoints(req.Address, "post")
	s.node.TryCommunityRewardSettle()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "post_id": id})
}

// handleCommunityLike POST /api/community/post/{id}/like {address}
func (s *RPCServer) handleCommunityLike(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "貼文 id 無效")
		return
	}
	addr, err := communityAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cm.Like(id, addr); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.node.addRewardPoints(addr, "like")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCommunityComments GET /api/community/post/{id}/comments
func (s *RPCServer) handleCommunityComments(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "貼文 id 無效")
		return
	}
	items, err := cm.Comments(id, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

// handleCommunityComment POST /api/community/post/{id}/comments {address,body}
func (s *RPCServer) handleCommunityComment(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "貼文 id 無效")
		return
	}
	var req struct {
		Address string `json:"address"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	if err := cm.AddComment(id, req.Address, req.Body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.node.addRewardPoints(req.Address, "comment")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCommunityReview POST /api/community/market/{id}/review {address,rating,comment}
func (s *RPCServer) handleCommunityReview(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "無效貼文 ID")
		return
	}
	var req struct {
		Address string `json:"address"`
		Rating  int    `json:"rating"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON 解析失敗")
		return
	}
	if err := cm.AddReview(id, req.Address, req.Rating, req.Comment); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	reviews, _ := cm.ListReviews(id)
	avg, n, _ := cm.PostRating(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reviews": reviews, "avg": avg, "count": n})
}

// handleCommunityReviews GET /api/community/market/{id}/reviews
func (s *RPCServer) handleCommunityReviews(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "無效貼文 ID")
		return
	}
	reviews, err := cm.ListReviews(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	avg, n, err := cm.PostRating(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reviews": reviews, "avg": avg, "count": n})
}

// handleCommunityMarket GET /api/community/market
func (s *RPCServer) handleCommunityMarket(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	items, err := cm.Market(50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type itemView struct {
		VideoURL  *string  `json:"video_url,omitempty"`
		ID        int64    `json:"id"`
		Address   string   `json:"address"`
		Kind      string   `json:"kind"`
		Content   string   `json:"content"`
		ImageURL  *string  `json:"image_url,omitempty"`
		CreatedAt int64    `json:"created_at"`
		PriceTACM *float64 `json:"price_tacm,omitempty"`
		Sold      bool     `json:"sold"`
		Buyer     string   `json:"buyer,omitempty"`
		Likes     int64    `json:"likes"`
		Comments  int64    `json:"comments"`
		Author    string   `json:"author"`
		Rating    float64  `json:"rating"`
		RCount    int      `json:"rating_count"`
	}
	out := make([]itemView, 0, len(items))
	for _, it := range items {
		avg, n, _ := cm.PostRating(it.ID)
		out = append(out, itemView{
			ID: it.ID, Address: it.Address, Kind: it.Kind, Content: it.Content,
			ImageURL: it.ImageURL, VideoURL: it.VideoURL, CreatedAt: it.CreatedAt, PriceTACM: it.PriceTACM,
			Sold: it.Sold, Buyer: it.Buyer, Likes: it.Likes, Comments: it.Comments,
			Author: it.Author, Rating: avg, RCount: n,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": out})
}

// handleCommunityMarketCreate POST /api/community/market/create {address,title,body,price_tacm,image_url}
func (s *RPCServer) handleCommunityMarketCreate(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	var req struct {
		Address  string  `json:"address"`
		Title    string  `json:"title"`
		Body     string  `json:"body"`
		PriceTCM float64 `json:"price_tacm"`
		ImageURL string  `json:"image_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	if req.Title == "" || req.PriceTCM <= 0 {
		writeErr(w, http.StatusBadRequest, "請輸入商品與價格")
		return
	}
	content := strings.TrimSpace(req.Title + "\n" + req.Body)
	id, err := cm.CreatePost(req.Address, "market", content, req.ImageURL, "", &req.PriceTCM)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item_id": id})
}

// handleCommunityBuyIntent GET /api/community/market/{id}/buy-intent?address=
// 買家意向查詢：回傳賣家地址、價格、memo、預估鏈上費（前端確認後走 /buy 一鍵付款）。
func (s *RPCServer) handleCommunityBuyIntent(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "item id 無效")
		return
	}
	buyer, err := communityAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := cm.GetPost(id)
	if err != nil || p == nil || p.Kind != "market" {
		writeErr(w, http.StatusNotFound, "商品不存在")
		return
	}
	price := 0.0
	if p.PriceTACM != nil {
		price = *p.PriceTACM
	}
	fee := 0.0
	if s.node != nil && s.node.walletSvc != nil {
		if amt, aerr := wallet.NewAmount(strconv.FormatFloat(price, 'f', -1, 64), wallet.AssetTACm); aerr == nil {
			feeWei := wallet.FeeOf(amt.Big, wallet.AssetTACm.FeeBps())
			bf := new(big.Float).SetInt(feeWei)
			fee, _ = new(big.Float).Quo(bf, big.NewFloat(1e18)).Float64()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "to": p.Address, "amount_tacm": price, "fee_tacm": fee,
		"memo": fmt.Sprintf("market#%d:buyer:%s", id, buyer),
	})
}

// handleCommunityBuy POST /api/community/market/{id}/buy {address}
// 一鍵購買：買家錢包直接鏈上轉帳 TACM 至賣家（手續費 2% 入資金池），回傳付款憑證。
func (s *RPCServer) handleCommunityBuy(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	if s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "錢包未啟用")
		return
	}
	id, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "item id 無效")
		return
	}
	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	buyer := strings.TrimSpace(req.Address)
	if buyer == "" {
		writeErr(w, http.StatusBadRequest, "address 必填")
		return
	}
	p, err := cm.GetPost(id)
	if err != nil || p == nil || p.Kind != "market" {
		writeErr(w, http.StatusNotFound, "商品不存在")
		return
	}
	if strings.EqualFold(buyer, p.Address) {
		writeErr(w, http.StatusBadRequest, "不能購買自己的商品")
		return
	}
	price := 0.0
	if p.PriceTACM != nil {
		price = *p.PriceTACM
	}
	amount, err := wallet.NewAmount(strconv.FormatFloat(price, 'f', -1, 64), wallet.AssetTACm)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "商品金額無效")
		return
	}
	memo := fmt.Sprintf("market#%d:buyer:%s", id, buyer)
	// 原子成交標記（防重複購買）：先保留商品，付款失敗則回滾釋放。
	claimed, err := cm.MarkSold(id, buyer)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !claimed {
		writeErr(w, http.StatusConflict, "商品已售出")
		return
	}
	fee, err := s.node.walletSvc.Transfer(buyer, p.Address, wallet.AssetTACm, amount, memo)
	if err != nil {
		_ = cm.UnmarkSold(id, buyer)
		writeErr(w, http.StatusBadRequest, "付款失敗："+err.Error())
		return
	}
	// 鏈上費自動結算入資金池（手續費照機制打入池，M21-B 一致；內部移轉免二次費）。
	if fee.Cmp(wallet.Amount{}) > 0 {
		poolMemo := memo + ":pool-fee"
		if perr := s.node.walletSvc.InternalTransfer(exchange.FeeUID, wallet.RewardPoolAddr, wallet.AssetTACm, fee, poolMemo); perr != nil {
			writeErr(w, http.StatusInternalServerError, "付款成功但費結算暫緩（費已留 fee 帳戶可 topup）："+perr.Error())
			return
		}
	}
	// M73-B：成交買方累積互動積分（鏈上獎勵結算用）。
	s.node.addRewardPoints(buyer, "sold")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "status": "paid", "to": p.Address,
		"amount_tacm": price, "fee_tacm": fee, "memo": memo,
	})
}

// handleCommunityFollow POST/DELETE /api/community/follow；GET /api/community/following?address=
// M73-B：會員追蹤（動態牆資料來源）。
func (s *RPCServer) handleCommunityFollow(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	var req struct {
		Follower string `json:"follower"`
		Followee string `json:"followee"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	follower := strings.TrimSpace(req.Follower)
	followee := strings.TrimSpace(req.Followee)
	if follower == "" || followee == "" {
		writeErr(w, http.StatusBadRequest, "follower/followee 必填")
		return
	}
	if strings.EqualFold(follower, followee) {
		writeErr(w, http.StatusBadRequest, "不能追蹤自己")
		return
	}
	switch r.Method {
	case http.MethodPost:
		if err := cm.Follow(follower, followee); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "following": true})
	case http.MethodDelete:
		if err := cm.Unfollow(follower, followee); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "following": false})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleCommunityFollowing GET /api/community/following?address=
func (s *RPCServer) handleCommunityFollowing(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "address 必填")
		return
	}
	items, err := cm.Following(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

// handleCommunityFollowers GET /api/community/followers?address=
// M95：反向追蹤（誰追蹤我）。
func (s *RPCServer) handleCommunityFollowers(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "address 必填")
		return
	}
	items, err := cm.Followers(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

// handleCommunityNotifications GET /api/community/notifications?address=&limit=
// M95：社群通知中心（讚/留言/追蹤/積分聚合）。
func (s *RPCServer) handleCommunityNotifications(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "address 必填")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	items, err := cm.Notifications(addr, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

// handleCommunityRewards GET /api/community/rewards?address=
// M73-B：我的互動積分＋排行＋結算期號。
func (s *RPCServer) handleCommunityRewards(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	s.node.TryCommunityRewardSettle()
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	myPoints := int64(0)
	if addr != "" {
		myPoints, _ = cm.Points(addr)
	}
	top, _ := cm.TopRewards(20)
	meta, _ := cm.GetRewardMeta()
	poolAddr, _ := s.node.CommunityPoolAddress()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "my_points": myPoints, "top": top,
		"period": meta.Period, "last_tip": meta.LastTip,
		"interval": CommunityRewardInterval, "per_period": CommunityRewardPerPeriod,
		"pool_address": poolAddr,
	})
}

// handleCommunitySettle POST /api/community/settle
// M73-B：手動觸發結算（達週期且有積分才會真正上鏈）。
func (s *RPCServer) handleCommunitySettle(w http.ResponseWriter, r *http.Request) {
	if s.node == nil {
		writeErr(w, http.StatusServiceUnavailable, "節點未就緒")
		return
	}
	n, err := s.node.SettleCommunityRewards(s.node.db.GetTipHeight())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settled": n})
}

// handleCommunityAdPay POST /api/community/ads/{id}/pay {address}
// 廣告主一鍵付款：廣告預算全額入資金池（內部移轉、免鏈上費促銷），廣告轉為投放中。
func (s *RPCServer) handleCommunityAdPay(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	if s.node == nil || s.node.walletSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "錢包未啟用")
		return
	}
	id, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ad id 無效")
		return
	}
	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	ad, err := cm.GetAd(id)
	if err != nil || ad == nil {
		writeErr(w, http.StatusNotFound, "廣告不存在")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(req.Address), ad.Address) {
		writeErr(w, http.StatusForbidden, "僅廣告主可付款")
		return
	}
	if ad.Status == "active" {
		writeErr(w, http.StatusConflict, "廣告已投放中")
		return
	}
	amount, err := wallet.NewAmount(strconv.FormatFloat(ad.BudgetTACM, 'f', -1, 64), wallet.AssetTACm)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "廣告預算無效")
		return
	}
	memo := fmt.Sprintf("ad#%d:pay", id)
	if err := s.node.walletSvc.InternalTransfer(ad.Address, wallet.RewardPoolAddr, wallet.AssetTACm, amount, memo); err != nil {
		writeErr(w, http.StatusBadRequest, "付款失敗："+err.Error())
		return
	}
	if err := cm.MarkAdPaid(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.node.addRewardPoints(ad.Address, "ad")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "status": "active", "ad_id": id,
		"amount_tacm": ad.BudgetTACM, "pool": wallet.RewardPoolAddr, "memo": memo,
	})
}

// handleCommunityAds GET /api/community/ads
func (s *RPCServer) handleCommunityAds(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	items, err := cm.Ads(20)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

// handleCommunityAdsCreate POST /api/community/ads/create {address,title,body,budget_tacm,target}
func (s *RPCServer) handleCommunityAdsCreate(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	var req struct {
		Address         string  `json:"address"`
		Title           string  `json:"title"`
		Body            string  `json:"body"`
		MediaURL        string  `json:"media_url"`
		BudgetTACM      float64 `json:"budget_tacm"`
		DailyBudgetTACM float64 `json:"daily_budget_tacm"`
		Placements      string  `json:"placements"`
		Target          string  `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	id, err := cm.CreateAd(req.Address, req.Title, req.Body, req.MediaURL, req.Target, req.Placements, req.BudgetTACM, req.DailyBudgetTACM)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 廣告預算付款建議：直接轉至廣告池（＝獎勵池／交易所資金池）。
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "ad_id": id, "pay_to": wallet.RewardPoolAddr,
		"amount_tacm": req.BudgetTACM, "memo": fmt.Sprintf("ad#%d:%s", id, req.Address),
	})
}

// handleCommunityAdImpression POST /api/community/ads/{id}/impression — 曝光計數。
func (s *RPCServer) handleCommunityAdImpression(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "廣告 ID 錯誤")
		return
	}
	if err := cm.AdImpression(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCommunityAdClick POST /api/community/ads/{id}/click — 點擊計數。
func (s *RPCServer) handleCommunityAdClick(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "廣告 ID 錯誤")
		return
	}
	if err := cm.AdClick(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCommunityTransferExchange POST /api/community/transfer/exchange {address,amount}
// 社群快捷入金：錢包地址 → 交易所（語義對齊原本 /api/transfer/exchange）。
func (s *RPCServer) handleCommunityTransferExchange(w http.ResponseWriter, r *http.Request) {
	if s.node.walletSvc == nil || s.node.exchangeSvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "wallet_or_exchange_disabled")
		return
	}
	var req struct {
		Address string `json:"address"`
		Amount  string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	a, aok := exchange.NormAssetEx("TACM")
	if !aok {
		writeErr(w, http.StatusInternalServerError, "TACM 資產異常")
		return
	}
	amt := mustAmt(req.Amount, a)
	if req.Address == "" || amt.IsZero() {
		writeErr(w, http.StatusBadRequest, "address_or_amount_required")
		return
	}
	wa, _ := wallet.NormalizeAsset("TACM")
	wfee, err := s.node.walletSvc.Transfer(req.Address, ExchangeVault, wa, toWalletAmt(amt), "community deposit")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "wallet 扣款失敗: "+err.Error())
		return
	}
	if err := s.node.exchangeSvc.Deposit(req.Address, a, amt, "from community"); err != nil {
		if _, rbErr := s.node.walletSvc.Transfer(ExchangeVault, req.Address, wa, toWalletAmt(amt), "rollback community deposit"); rbErr == nil {
			writeErr(w, http.StatusInternalServerError, "exchange 入帳失敗（已回滾）: "+err.Error())
		} else {
			writeErr(w, http.StatusInternalServerError, "exchange 入帳失敗且回滾異常: "+rbErr.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "address": req.Address, "amount": req.Amount,
		"chain_fee": wfee.String(wallet.AssetTACm),
	})
}

// handleCommunityStats GET /api/community/stats
func (s *RPCServer) handleCommunityStats(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	st, err := cm.Stats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "stats": st})
}

// handleCommunityConfig GET /api/community/config
func (s *RPCServer) handleCommunityConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "ad_pool_addr": wallet.RewardPoolAddr,
		"note": "廣告預算與市集付款皆以 TACM 鏈上轉帳完成；社群身分＝錢包地址",
	})
}

// community 回傳社群 store（若未初始化）。
func (s *RPCServer) community() (*community.Store, bool) {
	if s.node == nil || s.node.communitySvc == nil {
		return nil, false
	}
	return s.node.communitySvc, true
}

// postIDPath 從路徑 {id} 取貼文 id（Go 1.22+ PathValue）。
func postIDPath(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("bad id")
	}
	return id, nil
}

// handleLiveCreate POST /api/live/create {address,title}
func (s *RPCServer) handleLiveCreate(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	var req struct {
		Address string `json:"address"`
		Title   string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	id, err := cm.CreateLiveRoom(req.Address, req.Title)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "room_id": id})
}

// handleLiveList GET /api/live/list
func (s *RPCServer) handleLiveList(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	rooms, err := cm.ListLiveRooms(50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rooms": rooms})
}

// handleLiveMessages GET /api/live/{id}/messages?after=
func (s *RPCServer) handleLiveMessages(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid room id")
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if _, err := cm.LiveRoomByID(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	msgs, err := cm.LiveMessages(id, after, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": msgs})
}

// handleLivePost POST /api/live/{id}/message {address,body}
func (s *RPCServer) handleLivePost(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid room id")
		return
	}
	var req struct {
		Address string `json:"address"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	msgID, err := cm.PostLiveMessage(id, req.Address, req.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message_id": msgID})
}

// handleLiveEnd POST /api/live/{id}/end {address}
func (s *RPCServer) handleLiveEnd(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid room id")
		return
	}
	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	if err := cm.EndLiveRoom(id, req.Address); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMeAssets GET /api/me/assets?address= — 資產打通：錢包＋交易所＋社群獎勵。
func (s *RPCServer) handleMeAssets(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "address_required")
		return
	}
	out := map[string]any{
		"ok": true, "address": addr,
		"wallet":    map[string]any{"tacm": "0", "tiusd": "0", "usdt": "0"},
		"exchange":  map[string]any{"available": map[string]any{"TACM": "0", "TiUSD": "0", "USDT": "0"}, "frozen": map[string]any{"TACM": "0", "TiUSD": "0", "USDT": "0"}},
		"community": map[string]any{"points": 0, "rewards_tacm": "0"},
	}
	// 錢包
	if s.node.walletSvc != nil {
		if acc, err := s.node.walletSvc.Balance(addr); err == nil {
			out["wallet"] = map[string]any{
				"tacm":  wallet.FormatAmountBig(acc.TACmBalance),
				"tiusd": wallet.FormatAmountI64(acc.TiUSDBalance, wallet.AssetTiUSD),
				"usdt":  wallet.FormatAmountI64(acc.USDTBalance, wallet.AssetUSDT),
			}
		}
	}
	// 交易所
	if s.node.Exchange() != nil {
		if bals, err := s.node.Exchange().Balances(addr); err == nil {
			avail := map[string]string{}
			frozen := map[string]string{}
			for _, b := range bals {
				avail[string(b.Asset)] = b.AvailStr
				frozen[string(b.Asset)] = b.LockedStr
			}
			out["exchange"] = map[string]any{"available": avail, "frozen": frozen}
		}
	}
	// 社群積分與獎勵池
	if cm, ok := s.community(); ok {
		if pts, err := cm.Points(addr); err == nil {
			out["community"].(map[string]any)["points"] = pts
		}
		if poolAddr, err := s.node.CommunityPoolAddress(); err == nil {
			out["community"].(map[string]any)["pool_address"] = poolAddr
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCommunityProfileMeta POST /api/community/profile/meta {address,display_name,avatar_url}
func (s *RPCServer) handleCommunityProfileMeta(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	var req struct {
		Address     string `json:"address"`
		DisplayName string `json:"display_name"`
		AvatarURL   string `json:"avatar_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	if req.Address == "" {
		writeErr(w, http.StatusBadRequest, "address 必填")
		return
	}
	if len(req.DisplayName) > 40 {
		writeErr(w, http.StatusBadRequest, "顯示名稱過長（最多 40 字）")
		return
	}
	if len(req.AvatarURL) > 500 {
		writeErr(w, http.StatusBadRequest, "頭像網址過長")
		return
	}
	if err := cm.SetProfile(req.Address, req.DisplayName, req.AvatarURL); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCommunityProfileGet GET /api/community/profile/meta?address=...
func (s *RPCServer) handleCommunityProfileGet(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	addr := r.URL.Query().Get("address")
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "address 必填")
		return
	}
	pr, err := cm.Profile(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pr)
}

// handleCommunityPostEdit POST /api/community/post/{id}/edit {address,content}
func (s *RPCServer) handleCommunityPostEdit(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "貼文 id 無效")
		return
	}
	var req struct {
		Address string `json:"address"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "參數解析失敗")
		return
	}
	if req.Address == "" {
		writeErr(w, http.StatusBadRequest, "address 必填")
		return
	}
	ok2, err := cm.UpdatePostContent(id, req.Address, req.Content)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok2 {
		writeErr(w, http.StatusForbidden, "僅作者可編輯此貼文")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCommunityPostDelete POST /api/community/post/{id}/delete {address}
func (s *RPCServer) handleCommunityPostDelete(w http.ResponseWriter, r *http.Request) {
	cm, ok := s.community()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "社群未啟用")
		return
	}
	id, err := postIDPath(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "貼文 id 無效")
		return
	}
	addr, err := communityAddr(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ok2, err := cm.DeletePost(id, addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok2 {
		writeErr(w, http.StatusForbidden, "僅作者可刪除此貼文")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
