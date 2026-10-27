package copilot

import "github.com/Ryheff24/agentlog/internal/transcript"

type modelMetric struct {
	Usage struct {
		Input      int64 `json:"inputTokens"`
		Output     int64 `json:"outputTokens"`
		CacheRead  int64 `json:"cacheReadTokens"`
		CacheWrite int64 `json:"cacheWriteTokens"`
		Reasoning  int64 `json:"reasoningTokens"`
	} `json:"usage"`
	Requests struct {
		Count int `json:"count"`
	} `json:"requests"`
}

// Older CLI metrics reset on resume. Newer tokenDetails records restore
// lifetime metrics (including on idle resumes), so their latest snapshot wins.
// A repeated shutdown within one segment replaces rather than adds its totals.
type usageAccount struct{ completed, segment, cumulative transcript.Usage }

func (a *usageAccount) resume() {
	addUsage(&a.completed, a.segment)
	a.segment = transcript.Usage{}
}

func (a *usageAccount) shutdown(metrics map[string]modelMetric, cumulative bool) {
	if len(metrics) == 0 {
		return
	}
	u := transcript.Usage{}
	for name, m := range metrics {
		u.Input += max(int64(0), m.Usage.Input-m.Usage.CacheRead-m.Usage.CacheWrite)
		u.CacheRead += m.Usage.CacheRead
		u.CacheWrite += m.Usage.CacheWrite
		u.Output += m.Usage.Output
		u.Reasoning += m.Usage.Reasoning
		u.Total += m.Usage.Input + m.Usage.Output
		u.Requests += m.Requests.Count
		u.Model(name, m.Usage.Input+m.Usage.Output)
	}
	if cumulative {
		a.cumulative = u
	} else {
		a.segment = u
	}
}

func (a *usageAccount) usage() transcript.Usage {
	if a.cumulative.Known() {
		u := a.cumulative
		u.Source = "session.shutdown cumulative metrics"
		return u
	}
	u := a.completed
	addUsage(&u, a.segment)
	if u.Known() {
		u.Source = "session.shutdown segment metrics"
	}
	return u
}

func addUsage(u *transcript.Usage, v transcript.Usage) {
	u.Input += v.Input
	u.Output += v.Output
	u.CacheRead += v.CacheRead
	u.CacheWrite += v.CacheWrite
	u.Reasoning += v.Reasoning
	u.Total += v.Total
	u.Requests += v.Requests
	for model, tokens := range v.ByModel {
		u.Model(model, tokens)
	}
}
