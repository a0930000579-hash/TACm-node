package node

import (
	"log"
	"net/http"
	"net/http/pprof"
	"os"
)

// StartProfiler 當環境變數 TACM_PPROF_PORT 有設定時，僅在本機回環位址（127.0.0.1）
// 啟用 pprof 端點，不對外暴露。預設關閉、零行為變更，用於 CPU/記憶體現場診斷。
func (n *Node) StartProfiler() {
	port := os.Getenv("TACM_PPROF_PORT")
	if port == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	addr := "127.0.0.1:" + port
	go func() {
		log.Printf("pprof 端點已啟用（僅本機）http://%s/debug/pprof/", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("pprof 端點結束: %v", err)
		}
	}()
}
