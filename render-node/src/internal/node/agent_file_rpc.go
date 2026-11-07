package node

// agent_file_rpc.go — 生成檔案下載端點。
// GET /api/ai/file/{id}：以 attachment 回傳，瀏覽器直接觸發下載。

import (
	"net/http"
	"strconv"
)

func (s *RPCServer) handleAIDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f, ok := s.getFileStore().Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "file_not_found: 檔案已過期或不存在")
		return
	}
	w.Header().Set("Content-Type", f.MIME)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+f.Filename+"\"")
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Content)))
	_, _ = w.Write(f.Content)
}
