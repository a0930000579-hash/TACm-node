package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// v166：錢包地址提示與降級後 Model 反映實際服務層。

type captureProvider struct {
	msgs   []ChatMessage
	answer string
}

func (c *captureProvider) Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error) {
	c.msgs = msgs
	return Turn{Answer: c.answer}, nil
}
func (c *captureProvider) Name() string { return "capture" }

// (c) WithAddress 把連結地址寫進 system prompt；無地址時明確寫「未連結」。
func TestAgent_WithAddress_Note(t *testing.T) {
	p := &captureProvider{answer: "ok"}
	ag, err := NewAgent(p, NewRegistry(), WithAddress("tx0ABC-linked"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Run(context.Background(), "hi", "s1", false); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range p.msgs {
		if m.Role == "system" && strings.Contains(m.Content, "tx0ABC-linked") {
			found = true
		}
	}
	if !found {
		t.Fatalf("system messages should contain linked address, msgs=%+v", p.msgs)
	}

	p2 := &captureProvider{answer: "ok"}
	ag2, _ := NewAgent(p2, NewRegistry())
	if _, err := ag2.Run(context.Background(), "hi", "s2", false); err != nil {
		t.Fatal(err)
	}
	found2 := false
	for _, m := range p2.msgs {
		if m.Role == "system" && strings.Contains(m.Content, "未連結") {
			found2 = true
		}
	}
	if !found2 {
		t.Fatalf("system messages should say wallet not linked, msgs=%+v", p2.msgs)
	}
}

type errProvider struct{ err error }

func (e *errProvider) Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error) {
	return Turn{}, e.err
}
func (e *errProvider) Name() string { return "primary" }

type okProvider struct{ name string }

func (o *okProvider) Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error) {
	return Turn{Answer: "fallback-answer"}, nil
}
func (o *okProvider) Name() string { return o.name }

// P1：primary 失敗、fallback 服務後，Name() 應反映實際服務層（fallback），而非 primary。
func TestFallback_ActiveName(t *testing.T) {
	f := NewFallbackProvider(&errProvider{err: errors.New("ai: openai HTTP 401")}, &okProvider{name: "rules"})
	if _, err := f.Complete(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.Name(); got != "rules" {
		t.Fatalf("after fallback, Name should be rules, got %s", got)
	}

	// 成功服務時 Name() 應回 primary。
	okP := &okProvider{name: "primary"}
	f2 := NewFallbackProvider(okP, &okProvider{name: "rules"})
	if _, err := f2.Complete(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := f2.Name(); got != "primary" {
		t.Fatalf("when primary serves, Name should be primary, got %s", got)
	}
}
