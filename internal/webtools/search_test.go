package webtools

import (
	"testing"
)

func TestSearchBing(t *testing.T) {
	results, err := Search("今日科技新闻", 3, "")
	if err != nil {
		t.Logf("Search returned error (network dependent): %v", err)
		return
	}
	t.Logf("Got %d results", len(results))
	for i, r := range results {
		t.Logf("[%d] %s -> %s (%s)", i+1, r.Title, r.URL, r.Snippet)
	}
	if len(results) > 0 {
		md := FormatResultsAsMarkdown("今日科技新闻", results)
		t.Logf("Markdown format:\n%s", md)
	}
}
