package main

import (
	"reflect"
	"testing"
)

// TestResolveSnapshotURLs 快照多來源解析（M149r）：逗號分隔＋端點補全＋主網 URL 展開。
func TestResolveSnapshotURLs(t *testing.T) {
	// 顯式多來源：主機地址自動補 /api/snapshot，完整端點保留。
	urls, explicit := resolveSnapshotURLs("http://a.example, http://b.example/api/snapshot ,http://c.example/", "")
	if !explicit {
		t.Fatal("顯式 snapshot-url 應標記 explicit")
	}
	want := []string{
		"http://a.example/api/snapshot",
		"http://b.example/api/snapshot",
		"http://c.example/api/snapshot",
	}
	if !reflect.DeepEqual(urls, want) {
		t.Fatalf("解析結果=%v want %v", urls, want)
	}

	// 未指定 snapshot-url：從 mainnet-url 逗號列表展開。
	urls2, explicit2 := resolveSnapshotURLs("", "http://m1.example, http://m2.example")
	if explicit2 {
		t.Fatal("自動展開不應標記 explicit")
	}
	want2 := []string{"http://m1.example/api/snapshot", "http://m2.example/api/snapshot"}
	if !reflect.DeepEqual(urls2, want2) {
		t.Fatalf("主網展開=%v want %v", urls2, want2)
	}

	// 全空：無來源。
	urls3, _ := resolveSnapshotURLs("", "")
	if len(urls3) != 0 {
		t.Fatalf("空輸入應無來源: %v", urls3)
	}

	// 去空段（首尾逗號／連續逗號）。
	urls4, _ := resolveSnapshotURLs(" , http://x.example, ,", "")
	if len(urls4) != 1 || urls4[0] != "http://x.example/api/snapshot" {
		t.Fatalf("去空段失敗: %v", urls4)
	}

	// 已含端點不再重複補（大小寫不敏感匹配）。
	urls5, _ := resolveSnapshotURLs("http://y.example/API/SNAPSHOT", "")
	if len(urls5) != 1 || urls5[0] != "http://y.example/API/SNAPSHOT" {
		t.Fatalf("端點保留失敗: %v", urls5)
	}
}
