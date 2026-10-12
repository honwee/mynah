package instinct

import (
	"context"
	"strings"
)

// Rules is the zero-latency, zero-cost provider: keyword heuristics over the
// same closed questions. It is the floor the product stands on without a Jev
// key, and the fallback while Jev is slow, rate-limited or unreachable. It
// answers only the questions it understands (by option names in Criteria);
// unknown questions get no answer, which the gate treats as "no opinion".
type Rules struct{}

func (Rules) Name() string { return "rules" }

var (
	greetWords  = []string{"你好", "您好", "嗨", "哈喽", "hello", "hi", "早上好", "晚上好", "下午好", "我回来", "拜拜", "再见", "回见", "晚安"}
	agreeWords  = []string{"对", "是的", "没错", "好的", "嗯", "可以", "当然", "明白", "懂了", "同意", "真的", "就是", "太好了", "恭喜"}
	denyWords   = []string{"不", "别", "没有", "不行", "不是", "不要", "不对", "算了", "拒绝", "没办法"}
	thinkWords  = []string{"让我想想", "我想想", "想一下", "嗯…", "这个嘛", "怎么说呢", "有点难", "不确定", "可能"}
	cheerWords  = []string{"太棒了", "厉害", "恭喜", "耶", "成功", "通过了", "中了", "赢了", "哈哈", "开心"}
	sadWords    = []string{"难过", "累", "烦", "伤心", "哭", "崩溃", "糟糕", "失败", "压力", "委屈", "不开心"}
	praiseWords = []string{"好看", "漂亮", "可爱", "喜欢你", "爱你", "真美", "真好看", "好甜", "想你", "宝贝", "亲爱的", "厉害了"}
	teaseWords  = []string{"哈哈", "嘿嘿", "逗你", "开玩笑", "猜猜", "嘻嘻", "调皮", "坏笑"}
	energyHigh  = []string{"！", "!", "太", "超", "特别", "非常", "哈哈"}
	energyLow   = []string{"累", "困", "算了", "随便", "无所谓", "唉"}
)

func hasAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func (Rules) Decide(_ context.Context, state string, questions map[string]Question) (map[string]Answer, error) {
	low := strings.ToLower(state)
	out := map[string]Answer{}
	for name, q := range questions {
		switch q.Type {
		case "choice":
			choice, conf := ruleChoice(low, q.Criteria)
			if choice == "" {
				continue
			}
			out[name] = Answer{Type: "choice", Choice: choice, Confidence: conf, Probabilities: map[string]float64{choice: conf}}
		case "noul":
			// Only one yes/no question is asked today: "is the reply committing
			// to a physical action right now" — rules say yes when the reply
			// text literally announces it.
			p := 0.1
			if hasAny(low, []string{"挥手", "挥挥手", "点头", "点点头", "摇头", "摇摇头", "鞠躬", "比心", "wave", "nod"}) {
				p = 0.9
			}
			out[name] = Answer{Type: "noul", Noul: p}
		case "score":
			s := 0.5
			if hasAny(low, energyHigh) {
				s = 0.8
			}
			if hasAny(low, energyLow) {
				s = 0.25
			}
			out[name] = Answer{Type: "score", Score: s}
		}
	}
	return out, nil
}

// ruleChoice maps keyword hits onto whichever of the offered options exists.
// Options are the avatar's clip ids / reaction names; we only ever pick one
// that was offered.
func ruleChoice(low string, criteria map[string]string) (string, float64) {
	has := func(opt string) bool { _, ok := criteria[opt]; return ok }
	switch {
	case has("wave") && hasAny(low, greetWords):
		return "wave", 0.85
	case has("celebrate") && hasAny(low, cheerWords):
		return "celebrate", 0.8
	case has("laugh") && hasAny(low, cheerWords):
		return "laugh", 0.75
	case has("shy") && hasAny(low, praiseWords):
		return "shy", 0.8
	case has("wink") && hasAny(low, teaseWords):
		return "wink", 0.7
	case has("shake") && hasAny(low, denyWords):
		return "shake", 0.7
	case has("think") && hasAny(low, thinkWords):
		return "think", 0.75
	case has("comfort") && hasAny(low, sadWords):
		return "comfort", 0.75
	case has("nod") && hasAny(low, agreeWords):
		return "nod", 0.7
	case has("listen"):
		return "listen", 0.55
	case has("none"):
		return "none", 0.6
	}
	return "", 0
}
