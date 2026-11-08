package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestAttachmentsTextFile(t *testing.T) {
	dir := t.TempDir()
	old := uploadsBaseDir
	uploadsBaseDir = dir
	t.Cleanup(func() { uploadsBaseDir = old })

	fname := "0123456789abcdef0123456789abcdef.txt"
	if err := os.WriteFile(filepath.Join(dir, fname), []byte("這是檔案內文 123"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := "請幫我摘要 [attachment: note.txt /uploads/" + fname + "]"
	out := ingestAttachments(msg)
	if !strings.Contains(out, "這是檔案內文 123") {
		t.Fatalf("file content not embedded: %s", out)
	}
	if !strings.Contains(out, "[attached file: note.txt") {
		t.Fatalf("missing attachment header: %s", out)
	}
}

func TestIngestAttachmentsImageNeedsVision(t *testing.T) {
	msg := "[attachment: pic.png /uploads/0123456789abcdef0123456789abcdef.png]"
	out := ingestAttachments(msg)
	if !strings.Contains(out, "需視覺模型") {
		t.Fatalf("image should note vision requirement: %s", out)
	}
}

func TestIngestAttachmentsRejectsTraversal(t *testing.T) {
	msg := "[attachment: evil.txt /uploads/../etc/passwd]"
	out := ingestAttachments(msg)
	if strings.Contains(out, "root:") {
		t.Fatalf("path traversal should be blocked: %s", out)
	}
	if !strings.Contains(out, "附件路徑不合法") {
		t.Fatalf("invalid path should be flagged: %s", out)
	}
}

func TestIngestAttachmentsNoRefPassthrough(t *testing.T) {
	msg := "一般訊息沒有附件"
	if got := ingestAttachments(msg); got != msg {
		t.Fatalf("plain message changed: %s", got)
	}
}
