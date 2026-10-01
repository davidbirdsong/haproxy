package haharness

import "regexp"

var servedByRe = regexp.MustCompile(`\bbe1/(s\d+)\b`)

// ServedBy extracts every "be1/sN" server name mentioned in an haproxy
// access log blob (as produced by "option httplog"), in the order they
// appear. Useful for discovering which server name a given tenant key
// deterministically hashes to, so that mapping can later be hardcoded as a
// constant for stricter assertions.
func ServedBy(log string) []string {
	matches := servedByRe.FindAllStringSubmatch(log, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}
