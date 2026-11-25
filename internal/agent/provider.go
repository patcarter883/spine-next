package agent

import (
	"regexp"

	"github.com/patcarter883/spine-next/internal/api"
)

var providerPatterns = []struct {
	class api.ProviderFailure
	re    *regexp.Regexp
}{
	{api.ProviderAuth, regexp.MustCompile(`(?i)\b40[13]\b|unauthori[sz]ed|forbidden|authentication|invalid[ _-]?(x-)?api[ _-]?key|incorrect api key|no api key`)},
	{api.ProviderRateLimit, regexp.MustCompile(`(?i)\b429\b|rate[ _-]?limit|too many requests`)},
	{api.ProviderUnreachable, regexp.MustCompile(`(?i)connection error|connection refused|econnrefused|econnreset|enotfound|etimedout|request timed out|eai_again|fetch failed|socket hang up|\b50[234]\b|\b529\b|bad gateway|service unavailable|gateway timeout|overloaded`)},
}

// ProviderFailure classifies the error a run stopped with when it never
// reached the model: no tokens used and a provider error's signature. It
// returns "" for anything else, including an agent that failed to start.
func ProviderFailure(msg string, tokens int64) api.ProviderFailure {
	if tokens != 0 {
		return ""
	}
	for _, p := range providerPatterns {
		if p.re.MatchString(msg) {
			return p.class
		}
	}
	return ""
}
