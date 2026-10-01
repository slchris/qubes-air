package audit

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// Suppression-summary bounds (docs/runtime-defaults.md UD-24c).
const (
	// MaxTrackedSourcePrefixes bounds the distinct source prefixes one summary
	// counts. The prefixes are kept in memory until the summary is written, and
	// a caller rotating addresses must not be able to grow that without limit:
	// events from prefixes first seen after the cap are counted as untracked.
	MaxTrackedSourcePrefixes = 1024
	// TopSourcePrefixes is how many of the busiest prefixes a summary names.
	TopSourcePrefixes = 5
	// maxRawSource caps a source that is not an IP address (it always is when
	// it comes from gin's ClientIP; this is only a guard).
	maxRawSource = 64
)

// Suppression summarizes the sampled events the budget kept out of the store.
// Each of them still has its log line.
type Suppression struct {
	// First and Last bound the events' own times.
	First time.Time
	Last  time.Time
	Count int64
	// Sources is how many distinct source prefixes (SourcePrefix) the events
	// came from, exact up to MaxTrackedSourcePrefixes.
	Sources int
	// TopSources are the busiest of those prefixes, most events first (ties
	// by prefix), at most TopSourcePrefixes of them.
	TopSources []SourceCount
	// Untracked counts events from prefixes first seen after the
	// MaxTrackedSourcePrefixes cap was reached.
	Untracked int64
	// AuthDisabled reports that the console ran without authentication when
	// the events happened, as each event's own flag says.
	AuthDisabled bool
}

// SourceCount is one source prefix and how many suppressed events it sent.
type SourceCount struct {
	Prefix string
	Count  int64
}

// SourcesText renders the busiest prefixes for storage and the log:
// "198.51.100.0/24 4211; 2001:db8:1::/64 12; others 37", where "others" is
// every event not from a named prefix, the untracked ones included.
func (s Suppression) SourcesText() string {
	parts := make([]string, 0, len(s.TopSources)+1)
	named := int64(0)
	for _, sc := range s.TopSources {
		parts = append(parts, fmt.Sprintf("%s %d", sc.Prefix, sc.Count))
		named += sc.Count
	}
	if others := s.Count - named; others > 0 {
		parts = append(parts, fmt.Sprintf("others %d", others))
	}
	return strings.Join(parts, "; ")
}

// SourcePrefix is the network a suppressed event is attributed to: the /24 of
// an IPv4 address and the /64 of an IPv6 one. Aggregating by prefix is what
// keeps "who was probing" readable when a caller rotates addresses inside the
// block it controls; one host is usually alone in its /64.
func SourcePrefix(source string) string {
	addr, err := netip.ParseAddr(source)
	if err != nil {
		if len(source) > maxRawSource {
			source = source[:maxRawSource]
		}
		return validUTF8(source)
	}
	addr = addr.Unmap().WithZone("")
	bits := 64
	if addr.Is4() {
		bits = 24
	}
	prefix, err := addr.Prefix(bits)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}

// suppressionWindow accumulates the events one summary will stand for.
type suppressionWindow struct {
	s         Suppression
	prefixes  map[string]int64
	untracked int64
}

// add counts ev into the window.
func (w *suppressionWindow) add(ev Event) {
	if w.s.Count == 0 || ev.Time.Before(w.s.First) {
		w.s.First = ev.Time
	}
	if w.s.Count == 0 || ev.Time.After(w.s.Last) {
		w.s.Last = ev.Time
	}
	w.s.Count++
	w.s.AuthDisabled = w.s.AuthDisabled || ev.AuthDisabled
	if w.prefixes == nil {
		w.prefixes = map[string]int64{}
	}
	prefix := SourcePrefix(ev.Source)
	if _, seen := w.prefixes[prefix]; seen || len(w.prefixes) < MaxTrackedSourcePrefixes {
		w.prefixes[prefix]++
		return
	}
	w.untracked++
}

// take returns the window's summary and empties it. ok is false when nothing
// was suppressed.
func (w *suppressionWindow) take() (Suppression, bool) {
	s := w.s
	if s.Count == 0 {
		return Suppression{}, false
	}
	s.Sources = len(w.prefixes)
	s.Untracked = w.untracked
	s.TopSources = topSources(w.prefixes, TopSourcePrefixes)
	*w = suppressionWindow{}
	return s, true
}

// topSources returns the n busiest prefixes, most events first, ties broken
// by prefix so the result does not depend on map order.
func topSources(prefixes map[string]int64, n int) []SourceCount {
	all := make([]SourceCount, 0, len(prefixes))
	for prefix, count := range prefixes {
		all = append(all, SourceCount{Prefix: prefix, Count: count})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Count != all[j].Count {
			return all[i].Count > all[j].Count
		}
		return all[i].Prefix < all[j].Prefix
	})
	if len(all) > n {
		all = all[:n]
	}
	return all
}
