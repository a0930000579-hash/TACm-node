package community

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestFollowCRUD 追蹤/取消/查詢/冪等。
func TestFollowCRUD(t *testing.T) {
	s := openTestStore(t)
	a, b, c := "tx01aaaa", "tx01bbbb", "tx01cccc"
	if err := s.Follow(a, b); err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if err := s.Follow(a, b); err != nil { // 冪等
		t.Fatalf("Follow dup: %v", err)
	}
	if err := s.Follow(a, c); err != nil {
		t.Fatalf("Follow c: %v", err)
	}
	following, err := s.Following(a)
	if err != nil || len(following) != 2 {
		t.Fatalf("Following: %v / %d", err, len(following))
	}
	ok, err := s.IsFollowing(a, b)
	if err != nil || !ok {
		t.Fatalf("IsFollowing b: %v / %v", ok, err)
	}
	if err := s.Unfollow(a, b); err != nil {
		t.Fatalf("Unfollow: %v", err)
	}
	ok, _ = s.IsFollowing(a, b)
	if ok {
		t.Fatal("Unfollow 後仍為已追蹤")
	}
}

// TestFollowingFeed 追蹤動態牆只含自己＋已追蹤者貼文。
func TestFollowingFeed(t *testing.T) {
	s := openTestStore(t)
	alice, bob, carol := "tx01aaaa", "tx01bbbb", "tx01cccc"
	if _, err := s.CreatePost(alice, "post", "alice post", "", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePost(bob, "post", "bob post", "", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePost(carol, "post", "carol post", "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Follow(alice, bob); err != nil {
		t.Fatal(err)
	}
	items, err := s.FollowingFeed(alice, 50, 0)
	if err != nil {
		t.Fatalf("FollowingFeed: %v", err)
	}
	if len(items) != 2 { // alice 自己 + bob
		t.Fatalf("追蹤動態牆應 2 筆，得 %d", len(items))
	}
	for _, p := range items {
		if p.Address == carol {
			t.Fatal("追蹤動態牆混入非追蹤者貼文")
		}
	}
}

// TestRewardPointsAndSettle 積分累積、排行與結算快照清零。
func TestRewardPointsAndSettle(t *testing.T) {
	s := openTestStore(t)
	alice, bob := "tx01aaaa", "tx01bbbb"
	if err := s.AddPoints(alice, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPoints(alice, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPoints(bob, 2); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Points(alice)
	if p != 8 {
		t.Fatalf("alice 積分應 8，得 %d", p)
	}
	top, err := s.TopRewards(5)
	if err != nil || len(top) != 2 || top[0].Address != alice || top[0].Points != 8 {
		t.Fatalf("TopRewards 異常: %v %v", top, err)
	}
	rows, err := s.SnapshotAndClearRewards()
	if err != nil || len(rows) != 2 {
		t.Fatalf("快照異常: %v %v", rows, err)
	}
	p, _ = s.Points(alice)
	if p != 0 {
		t.Fatalf("結算後積分應清零，得 %d", p)
	}
	// 空快照再清一次不報錯。
	if rows, err := s.SnapshotAndClearRewards(); err != nil || len(rows) != 0 {
		t.Fatalf("空快照異常: %v %v", rows, err)
	}
}

// TestRewardMeta 結算期號持久化。
func TestRewardMeta(t *testing.T) {
	s := openTestStore(t)
	m, err := s.GetRewardMeta()
	if err != nil || m.Period != 0 {
		t.Fatalf("初始 meta 應為 0/0: %+v %v", m, err)
	}
	if err := s.SetRewardMeta(1, 60); err != nil {
		t.Fatal(err)
	}
	m, _ = s.GetRewardMeta()
	if m.Period != 1 || m.LastTip != 60 {
		t.Fatalf("meta 更新失敗: %+v", m)
	}
}

// TestOpenCompat 舊庫（無新表）相容：僅 posts 的庫可直接 Open。
func TestOpenCompat(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir); err != nil {
		t.Fatalf("新表建立失敗: %v", err)
	}
	if _, err := Open(filepath.Join(dir, "sub")); err != nil {
		t.Fatalf("重複 Open 失敗: %v", err)
	}
}

// TestMarketReviews：市集評價 CRUD（僅 market 貼文、1-5 分、每人一評更新）。
func TestMarketReviews(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	price := 10.0
	pid, err := st.CreatePost("tx0seller", "market", "新品上架", "", "", &price)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddReview(pid, "tx0buyer", 5, "非常好"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddReview(pid, "tx0buyer2", 3, "尚可"); err != nil {
		t.Fatal(err)
	}
	// 同一買家更新（upsert）
	if err := st.AddReview(pid, "tx0buyer", 4, "再想想"); err != nil {
		t.Fatal(err)
	}
	reviews, err := st.ListReviews(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 {
		t.Fatalf("應 2 則評價，got %d", len(reviews))
	}
	for _, r := range reviews {
		if r.Address == "tx0buyer" && (r.Rating != 4 || r.Comment != "再想想") {
			t.Fatalf("upsert 失敗: %+v", r)
		}
	}
	avg, n, err := st.PostRating(pid)
	if err != nil || n != 2 {
		t.Fatalf("統計失敗 avg=%v n=%d err=%v", avg, n, err)
	}
	if avg != 3.5 {
		t.Fatalf("平均應 3.5，got %v", avg)
	}
	// 非法分數 / 非市集貼文 → 報錯
	if err := st.AddReview(pid, "tx0x", 6, "x"); err == nil {
		t.Fatal("6 分應報錯")
	}
	postID, _ := st.CreatePost("tx0a", "post", "一般貼文", "", "", nil)
	if err := st.AddReview(postID, "tx0b", 5, "x"); err == nil {
		t.Fatal("非市集貼文應報錯")
	}
}

// TestSanitizeRichText：富文本白名單（保留 b/i/a，剝離 script/事件/style/javascript:）。
func TestSanitizeRichText(t *testing.T) {
	in := `<b>粗體</b><script>alert(1)</script><a href="javascript:alert(2)" onclick="x()">bad</a><i>斜</i><p style="color:red">p</p><img src=x onerror=alert(3)>`
	out := sanitizeRichText(in)
	for _, bad := range []string{"<script", "javascript:", "onclick", "onerror", "style=", "<img"} {
		if contains(out, bad) {
			t.Errorf("sanitize 未清除 %q: %s", bad, out)
		}
	}
	if !contains(out, "<b>粗體</b>") || !contains(out, "<i>斜</i>") {
		t.Errorf("白名單標籤被誤刪: %s", out)
	}
	if !contains(out, "<p>p</p>") {
		t.Errorf("p 標籤屬性未剝離保留標籤: %s", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestVideoPost 影片貼文：video_url 存讀＋feed 回傳。
func TestVideoPost(t *testing.T) {
	s := openTestStore(t)
	a := "tx01vid1"
	_, err := s.CreatePost(a, "video", "我的挖礦影片", "", "https://example.com/clip.mp4", nil)
	if err != nil {
		t.Fatalf("CreatePost video: %v", err)
	}
	items, err := s.Feed(10, 0)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].VideoURL == nil || *items[0].VideoURL != "https://example.com/clip.mp4" {
		t.Fatalf("video_url lost: %v", items[0].VideoURL)
	}
	if items[0].Kind != "video" {
		t.Fatalf("kind want video, got %s", items[0].Kind)
	}
}

// TestLiveCRUD 直播：開播→發訊息→輪詢→結束。
func TestLiveCRUD(t *testing.T) {
	s := openTestStore(t)
	owner, viewer := "tx01live1", "tx01live2"
	roomID, err := s.CreateLiveRoom(owner, "TAC 主網直播")
	if err != nil {
		t.Fatalf("CreateLiveRoom: %v", err)
	}
	if roomID <= 0 {
		t.Fatalf("bad room id")
	}
	// 開播者缺標題→拒
	if _, err := s.CreateLiveRoom(owner, "   "); err == nil {
		t.Fatal("empty title must fail")
	}
	if _, err := s.PostLiveMessage(roomID, viewer, "hello 大家好"); err != nil {
		t.Fatalf("PostLiveMessage: %v", err)
	}
	if _, err := s.PostLiveMessage(roomID, owner, "歡迎收看"); err != nil {
		t.Fatalf("PostLiveMessage owner: %v", err)
	}
	msgs, err := s.LiveMessages(roomID, 0, 50)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("LiveMessages: %v / %d", err, len(msgs))
	}
	// 增量輪詢 after=1 → 只剩第 2 條
	msgs2, err := s.LiveMessages(roomID, msgs[0].ID, 50)
	if err != nil || len(msgs2) != 1 || msgs2[0].Body != "歡迎收看" {
		t.Fatalf("incremental poll: %v / %d", err, len(msgs2))
	}
	rooms, err := s.ListLiveRooms(10)
	if err != nil || len(rooms) != 1 {
		t.Fatalf("ListLiveRooms: %v / %d", err, len(rooms))
	}
	if rooms[0].Viewers != 2 {
		t.Fatalf("viewers want 2, got %d", rooms[0].Viewers)
	}
	// 非開播者結束→拒
	if err := s.EndLiveRoom(roomID, viewer); err == nil {
		t.Fatal("non-owner end must fail")
	}
	if err := s.EndLiveRoom(roomID, owner); err != nil {
		t.Fatalf("EndLiveRoom: %v", err)
	}
	r, err := s.LiveRoomByID(roomID)
	if err != nil || r.Status != "ended" {
		t.Fatalf("status after end: %v / %s", err, r.Status)
	}
}
