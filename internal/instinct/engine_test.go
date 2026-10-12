package instinct

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeProv struct {
	name string
	ans  map[string]Answer
	err  error
	n    int
	mu   sync.Mutex
}

func (f *fakeProv) Name() string { return f.name }
func (f *fakeProv) Decide(context.Context, string, map[string]Question) (map[string]Answer, error) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	return f.ans, f.err
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestRulesWaveOnGreeting(t *testing.T) {
	var mu sync.Mutex
	played := []string{}
	e := New(Options{Provider: Rules{}, Clips: map[string]string{"wave": "挥手", "nod": "点头"},
		Act: func(id string) bool { mu.Lock(); played = append(played, id); mu.Unlock(); return true }})
	defer e.Close()
	e.UserSpeechStart()
	e.UserText("你好呀，我回来啦", true)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(played) == 1 })
	if played[0] != "wave" {
		t.Fatalf("got %v", played)
	}
}

func TestGateRespectsThresholdAndCooldown(t *testing.T) {
	var mu sync.Mutex
	played := 0
	p := &fakeProv{name: "p", ans: map[string]Answer{"clip": {Type: "choice", Choice: "nod", Confidence: 0.5}}}
	e := New(Options{Provider: p, Clips: map[string]string{"nod": "点头"}, Cooldown: time.Hour,
		Act: func(string) bool { mu.Lock(); played++; mu.Unlock(); return true }})
	defer e.Close()
	e.UserText("对对对没错就是这样", true)
	waitFor(t, func() bool { return len(e.Recent(1)) == 1 })
	if played != 0 || e.Recent(1)[0].Reason != "below threshold" {
		t.Fatalf("played=%d reason=%q", played, e.Recent(1)[0].Reason)
	}
	// confident now -> plays once; the second ask is blocked by cooldown
	p.ans = map[string]Answer{"clip": {Type: "choice", Choice: "nod", Confidence: 0.9}}
	e.UserSpeechStart()
	e.UserText("对对对没错就是这样啊", true)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return played == 1 })
	e.UserSpeechStart()
	e.UserText("再来一次对对对没错", true)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if played != 1 {
		t.Fatalf("cooldown ignored: played=%d", played)
	}
}

func TestFallbackWhenPrimaryFails(t *testing.T) {
	bad := &fakeProv{name: "jev", err: errors.New("451")}
	var mu sync.Mutex
	played := []string{}
	e := New(Options{Provider: bad, Fallback: Rules{}, Clips: map[string]string{"wave": "挥手"},
		Act: func(id string) bool { mu.Lock(); played = append(played, id); mu.Unlock(); return true }})
	defer e.Close()
	e.UserText("哈喽你好", true)
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(played) == 1 })
	d := e.Recent(1)[0]
	if d.Provider != "rules" || played[0] != "wave" {
		t.Fatalf("provider=%s played=%v", d.Provider, played)
	}
}

func TestSpeakingNeedsCommit(t *testing.T) {
	p := &fakeProv{name: "p", ans: map[string]Answer{
		"clip":   {Type: "choice", Choice: "wave", Confidence: 0.95},
		"commit": {Type: "noul", Noul: 0.2}}}
	played := 0
	e := New(Options{Provider: p, Clips: map[string]string{"wave": "挥手"}, Act: func(string) bool { played++; return true }})
	defer e.Close()
	e.AgentSpeechStart()
	e.AgentText("今天天气真不错呢，我们聊点别的吧", true)
	waitFor(t, func() bool { return len(e.Recent(1)) == 1 })
	e.Idle()
	time.Sleep(20 * time.Millisecond)
	if played != 0 {
		t.Fatal("played without commit")
	}
}

func TestReactionIsHeldUntilSpeechEnds(t *testing.T) {
	p := &fakeProv{name: "p", ans: map[string]Answer{
		"clip":     {Type: "choice", Choice: "none", Confidence: 0.9},
		"commit":   {Type: "noul", Noul: 0.05},
		"reaction": {Type: "choice", Choice: "shy", Confidence: 0.93}}}
	var mu sync.Mutex
	played := []string{}
	e := New(Options{Provider: p, Clips: map[string]string{"shy": "害羞"},
		Act: func(id string) bool { mu.Lock(); played = append(played, id); mu.Unlock(); return true }})
	defer e.Close()
	e.AgentSpeechStart()
	e.AgentText("你这么说，我都有点不好意思了啦", true)
	waitFor(t, func() bool { return len(e.Recent(1)) == 1 })
	d := e.Recent(1)[0]
	if d.Clip != "shy" || d.When != "after_speech" {
		t.Fatalf("decision=%+v", d)
	}
	mu.Lock()
	n := len(played)
	mu.Unlock()
	if n != 0 {
		t.Fatal("reaction played mid-speech")
	}
	e.Idle()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(played) == 1 && played[0] == "shy" })
}

func TestFinalUtteranceDuringInflightIsNotDropped(t *testing.T) {
	gate := make(chan struct{})
	slow := &slowProv{release: gate, ans: map[string]Answer{"clip": {Type: "choice", Choice: "none", Confidence: 0.9}}}
	e := New(Options{Provider: slow, Clips: map[string]string{"nod": "点头"}, MinInterval: time.Millisecond})
	defer e.Close()
	e.UserSpeechStart()
	e.UserText("第一句话还没说完呢", false) // starts a slow decision
	time.Sleep(20 * time.Millisecond)
	e.UserText("第一句话还没说完呢，这是最终的转录", true) // lands while in flight
	close(gate)
	waitFor(t, func() bool { slow.mu.Lock(); defer slow.mu.Unlock(); return slow.n == 2 })
}

type slowProv struct {
	release chan struct{}
	ans     map[string]Answer
	mu      sync.Mutex
	n       int
}

func (s *slowProv) Name() string { return "slow" }
func (s *slowProv) Decide(ctx context.Context, _ string, _ map[string]Question) (map[string]Answer, error) {
	s.mu.Lock()
	s.n++
	first := s.n == 1
	s.mu.Unlock()
	if first {
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	}
	return s.ans, nil
}

func TestPacerLimitsAsksPerTurn(t *testing.T) {
	p := &fakeProv{name: "p", ans: map[string]Answer{"clip": {Type: "choice", Choice: "none", Confidence: 0.9}}}
	e := New(Options{Provider: p, Clips: map[string]string{"nod": "点头"}, MinInterval: time.Millisecond, MaxPerTurn: 2})
	defer e.Close()
	e.UserSpeechStart()
	for i := 0; i < 10; i++ {
		e.UserText(string(make([]rune, 0))+"这是一段越来越长的转录文本第"+string(rune('0'+i))+"句话还在继续说下去呢", true)
		time.Sleep(15 * time.Millisecond)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.n != 2 {
		t.Fatalf("asks=%d want 2", p.n)
	}
}
