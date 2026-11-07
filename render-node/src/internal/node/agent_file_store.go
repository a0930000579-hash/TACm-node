package node

// agent_file_store.go — TACm AI 生成檔案的暫存與下載支援。
//
// 設計：
//   - 純記憶體、不落地（節點不持久化使用者檔案，重啟即清）。
//   - 有 TTL 與筆數上限，避免記憶體膨脹；超量逐出最舊檔。
//   - 執行緒安全；下載端點只讀。

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

const (
	agentFileTTL   = 30 * time.Minute
	agentFileMax   = 200
	agentFileMaxSz = 2 << 20 // 單檔 2 MiB
)

type generatedFile struct {
	ID        string
	Filename  string
	Language  string
	MIME      string
	Content   []byte
	CreatedAt int64
	expiresAt time.Time
}

type agentFileStore struct {
	mu    sync.Mutex
	files map[string]*generatedFile
}

func newAgentFileStore() *agentFileStore {
	return &agentFileStore{files: map[string]*generatedFile{}}
}

// getFileStore 惰性取得 RPCServer 的檔案暫存（單一實例）。
func (s *RPCServer) getFileStore() *agentFileStore {
	if s.fileStore == nil {
		s.fileStore = newAgentFileStore()
	}
	return s.fileStore
}

func newFileID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "af_" + hex.EncodeToString(b[:])
}

// Put 儲存一個生成檔案，回傳副本（含 id）。
func (st *agentFileStore) Put(filename, language, mime string, content []byte) (*generatedFile, error) {
	if len(content) == 0 {
		return nil, errAgentFile("file content is empty")
	}
	if len(content) > agentFileMaxSz {
		return nil, errAgentFile("file exceeds size limit")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked(time.Now())
	if len(st.files) >= agentFileMax {
		st.evictOldestLocked()
	}
	f := &generatedFile{
		ID: newFileID(), Filename: filename, Language: language, MIME: mime,
		Content: append([]byte(nil), content...),
		CreatedAt: time.Now().Unix(), expiresAt: time.Now().Add(agentFileTTL),
	}
	st.files[f.ID] = f
	return f, nil
}

// Get 取回未過期檔案。
func (st *agentFileStore) Get(id string) (*generatedFile, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, ok := st.files[id]
	if !ok || time.Now().After(f.expiresAt) {
		return nil, false
	}
	return f, true
}

func (st *agentFileStore) pruneLocked(now time.Time) {
	for id, f := range st.files {
		if now.After(f.expiresAt) {
			delete(st.files, id)
		}
	}
}

func (st *agentFileStore) evictOldestLocked() {
	all := make([]*generatedFile, 0, len(st.files))
	for _, f := range st.files {
		all = append(all, f)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt < all[j].CreatedAt })
	if len(all) > 0 {
		delete(st.files, all[0].ID)
	}
}

type agentFileErr string

func (e agentFileErr) Error() string { return string(e) }

func errAgentFile(s string) error { return agentFileErr(s) }
