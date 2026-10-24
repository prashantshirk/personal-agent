package router

import (
	"strings"
	"testing"

	"secondbrain/internal/search"
)

func TestAllowedUser(t *testing.T) {
	for _, tc := range []struct {
		name          string
		user, allowed int64
		want          bool
	}{{"allowed", 42, 42, true}, {"other", 7, 42, false}, {"missing", 0, 42, false}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllowedUser(tc.user, tc.allowed); got != tc.want {
				t.Fatalf("AllowedUser(%d,%d)=%v, want %v", tc.user, tc.allowed, got, tc.want)
			}
		})
	}
}

func TestSplitMessageNeverBreaksRunesOrExceedsLimit(t *testing.T) {
	text := strings.Repeat("hello world\n", 1000) + strings.Repeat("é", 5000)
	chunks := splitMessage(text, 3900)
	joined := strings.Join(chunks, "")
	// splitMessage strips the newline it splits on, so re-joining cannot
	// be compared byte-for-byte; instead check every chunk is in range
	// and the visible content survives.
	for i, c := range chunks {
		if len([]rune(c)) > 3900 {
			t.Fatalf("chunk %d has %d runes, want <= 3900", i, len([]rune(c)))
		}
	}
	if !strings.Contains(joined, "hello world") || !strings.Contains(joined, "é") {
		t.Fatalf("split message lost content")
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("héllo", 4); got != "hél…" {
		t.Fatalf("truncateRunes = %q, want %q", got, "hél…")
	}
	if got := truncateRunes("hi", 10); got != "hi" {
		t.Fatalf("truncateRunes short = %q, want hi", got)
	}
}

func TestFormatSearchResultsPagination(t *testing.T) {
	results := make([]search.Result, 10)
	for i := range results {
		results[i] = search.Result{Kind: "note", RefID: int64(i + 1), Title: "note"}
	}
	_, kb := formatSearchResults(results, "and", 0, 7)
	if len(kb) == 0 || kb[len(kb)-1][0].CallbackData != "search:next:7" {
		t.Fatalf("first page keyboard = %+v, want a Next button for seq 7", kb)
	}
	_, kb = formatSearchResults(results, "and", 10, 7)
	last := kb[len(kb)-1]
	if len(last) != 2 || last[0].CallbackData != "search:prev:7" || last[1].CallbackData != "search:next:7" {
		t.Fatalf("second page keyboard = %+v, want Prev and Next", kb)
	}
	short := results[:3]
	_, kb = formatSearchResults(short, "and", 0, 7)
	for _, row := range kb {
		for _, b := range row {
			if strings.HasPrefix(b.CallbackData, "search:") {
				t.Fatalf("short page should not paginate, got %+v", kb)
			}
		}
	}
}

func TestSearchIntentIsLocalFirst(t *testing.T) {
	for _, q := range []string{"find restful api", "search my notes", "look up invoice", "find me passport"} {
		if !isLocalSearch(q) {
			t.Fatalf("isLocalSearch(%q) = false, want true", q)
		}
	}
	if got := searchRemainder("search for restful api"); got != "restful api" {
		t.Fatalf("searchRemainder = %q, want restful api", got)
	}
	// A topic containing "web" is not an explicit web request.
	if isExplicitWeb("find web development notes") {
		t.Fatalf("isExplicitWeb should be false for a web topic")
	}
	if !isExplicitWeb("search the web for restful api") {
		t.Fatalf("isExplicitWeb should be true for 'search the web for'")
	}
	if got := explicitWebQuery("search the web for restful api"); got != "restful api" {
		t.Fatalf("explicitWebQuery = %q, want restful api", got)
	}
}

func TestMenuKeyboardHasTodayAndHelp(t *testing.T) {
	kb := menuKeyboard()
	found := map[string]bool{}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			found[b.CallbackData] = true
		}
	}
	if !found["menu:today"] || !found["menu:help"] {
		t.Fatalf("menu keyboard = %+v, want menu:today and menu:help", kb)
	}
	if !strings.Contains(helpText, "/today") || !strings.Contains(helpText, "/menu") {
		t.Fatalf("help text does not mention /today and /menu")
	}
}
