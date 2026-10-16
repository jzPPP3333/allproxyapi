package webtools

import (
	"crypto/tls"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/util"
)

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

var (
	tagRegex     = regexp.MustCompile(`(?s)<[^>]+>`)
	bAlgoRegex   = regexp.MustCompile(`(?is)<li class="b_algo".*?</li>`)
	linkRegex    = regexp.MustCompile(`(?is)<h2[^>]*>\s*<a[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	snippetRegex = regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`)
)

func StripTags(s string) string {
	clean := tagRegex.ReplaceAllString(s, " ")
	clean = html.UnescapeString(clean)
	return strings.Join(strings.Fields(clean), " ")
}

// Search queries cn.bing.com or duckduckgo to fetch live web results.
func Search(query string, count int, proxyURL string) ([]SearchResult, error) {
	if count <= 0 {
		count = 5
	}
	if count > 10 {
		count = 10
	}

	// Try Bing first
	results, err := searchBing(query, count, proxyURL)
	if err == nil && len(results) > 0 {
		return results, nil
	}

	// Fallback to DuckDuckGo
	return searchDuckDuckGo(query, count, proxyURL)
}

func searchBing(query string, count int, proxyURL string) ([]SearchResult, error) {
	reqURL := "https://cn.bing.com/search?q=" + url.QueryEscape(query)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7")

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	htmlStr := string(bodyBytes)
	blocks := bAlgoRegex.FindAllString(htmlStr, count*2)

	var results []SearchResult
	seen := make(map[string]bool)

	for _, block := range blocks {
		linkMatch := linkRegex.FindStringSubmatch(block)
		if len(linkMatch) < 3 {
			continue
		}
		href := linkMatch[1]
		title := StripTags(linkMatch[2])

		if !strings.HasPrefix(href, "http") || title == "" || seen[href] {
			continue
		}
		if strings.Contains(href, "bing.com/search") {
			continue
		}
		seen[href] = true

		snippet := ""
		snipMatch := snippetRegex.FindStringSubmatch(block)
		if len(snipMatch) >= 2 {
			snippet = StripTags(snipMatch[1])
		}

		results = append(results, SearchResult{
			Title:   title,
			URL:     href,
			Snippet: snippet,
		})

		if len(results) >= count {
			break
		}
	}

	return results, nil
}

func searchDuckDuckGo(query string, count int, proxyURL string) ([]SearchResult, error) {
	reqURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	effectiveProxy := util.DetectProxyURL(proxyURL)
	client := util.NewHTTPClient(effectiveProxy, false, 15*time.Second)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	htmlStr := string(bodyBytes)
	re := regexp.MustCompile(`(?is)<a class="result__snippet[^"]*"[^>]*>(.*?)</a>`)
	urlRe := regexp.MustCompile(`(?is)<a class="result__url"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)

	snippets := re.FindAllStringSubmatch(htmlStr, count)
	urls := urlRe.FindAllStringSubmatch(htmlStr, count)

	var results []SearchResult
	for i := 0; i < len(snippets) && i < len(urls); i++ {
		href := urls[i][1]
		// Decode duckduckgo redirect if present
		if strings.Contains(href, "uddg=") {
			if u, err := url.Parse(href); err == nil {
				if realURL := u.Query().Get("uddg"); realURL != "" {
					href = realURL
				}
			}
		}

		results = append(results, SearchResult{
			Title:   StripTags(urls[i][2]),
			URL:     href,
			Snippet: StripTags(snippets[i][1]),
		})
	}

	return results, nil
}

// FormatResultsAsMarkdown converts search results into a clean reference text block.
func FormatResultsAsMarkdown(query string, results []SearchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("网络搜索关键词: '%s'，未获取到有效实时网页结果。", query)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[网络实时搜索结果 - 关键词: %s (日期: %s)]\n",
		query, time.Now().Format("2006-01-02 15:04")))

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. 标题: %s\n", i+1, r.Title))
		if r.Snippet != "" {
			sb.WriteString(fmt.Sprintf("   摘要: %s\n", r.Snippet))
		}
		sb.WriteString(fmt.Sprintf("   链接: %s\n", r.URL))
	}

	return sb.String()
}
