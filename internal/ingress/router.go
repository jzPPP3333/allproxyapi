package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/config"
	"github.com/jzPPP3333/allproxyapi/internal/ingress/anthropic"
	"github.com/jzPPP3333/allproxyapi/internal/ingress/openai"
	"github.com/jzPPP3333/allproxyapi/internal/pool"
	"github.com/jzPPP3333/allproxyapi/internal/web"
	"github.com/jzPPP3333/allproxyapi/internal/webtools"
)

type Server struct {
	cfg       *config.Config
	pool      *pool.AccountPool
	upstreams *Registry
	mux       *http.ServeMux
}

func NewServer(cfg *config.Config, p *pool.AccountPool, upstreams *Registry) *Server {
	s := &Server{
		cfg:       cfg,
		pool:      p,
		upstreams: upstreams,
		mux:       http.NewServeMux(),
	}
	s.setupRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.loggingMiddleware(s.corsMiddleware(s.authMiddleware(s.mux)))
}

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *statusResponseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		duration := time.Since(start)

		if r.URL.Path != "/health" && r.URL.Path != "/api/status" && r.URL.Path != "/api/accounts" {
			log.Printf("[HTTP] %s %s -> 状态码:%d (耗时:%s, 客户端:%s)",
				r.Method, r.URL.Path, rw.statusCode, duration, r.RemoteAddr)
		}
	})
}

func (s *Server) setupRoutes() {
	// OpenAI compatibility
	s.mux.HandleFunc("/v1/models", s.handleModels)
	s.mux.HandleFunc("/models", s.handleModels)
	s.mux.HandleFunc("/v1/chat/completions", s.handleOpenAIChat)
	s.mux.HandleFunc("/chat/completions", s.handleOpenAIChat)
	s.mux.HandleFunc("/v1/responses", s.handleOpenAIChat)
	s.mux.HandleFunc("/responses", s.handleOpenAIChat)

	// Anthropic compatibility
	s.mux.HandleFunc("/v1/messages", s.handleAnthropicMessages)
	s.mux.HandleFunc("/messages", s.handleAnthropicMessages)
	s.mux.HandleFunc("/v1/messages/count_tokens", s.handleAnthropicCountTokens)

	// Health and Status
	s.mux.HandleFunc("/health", s.handleHealth)

	// Dashboard Management APIs
	s.mux.HandleFunc("/api/status", s.handleAPIStatus)
	s.mux.HandleFunc("/api/accounts", s.handleAPIAccounts)
	s.mux.HandleFunc("/api/accounts/cooldown/reset", s.handleAPIResetCooldown)
	s.mux.HandleFunc("/api/accounts/sync-models", s.handleAPISyncModels)
	s.mux.HandleFunc("/api/accounts/import", s.handleAPIImportAccount)
	s.mux.HandleFunc("/api/models", s.handleAPIModels)

	// Embedded Web Dashboard
	web.RegisterRoutes(s.mux)
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version, User-Agent")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow health and public web UI without auth
		if r.URL.Path == "/health" || r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/ui") || strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		expectedKey := s.cfg.Server.APIKey
		if expectedKey == "" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		apiKeyHeader := r.Header.Get("x-api-key")

		clientKey := ""
		if strings.HasPrefix(authHeader, "Bearer ") {
			clientKey = strings.TrimPrefix(authHeader, "Bearer ")
		} else if apiKeyHeader != "" {
			clientKey = apiKeyHeader
		}

		// If expectedKey is customized from default, enforce strict check
		if expectedKey != "sk-desktopproxy" {
			if clientKey != expectedKey {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]interface{}{
						"message": fmt.Sprintf("Invalid API Key: expected '%s'", expectedKey),
						"type":    "invalid_request_error",
						"code":    "invalid_api_key",
					},
				})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// ResolveProvider is declared in upstream.go next to the registry it consults.

// fetchModelsFor discovers the live catalog for an account via its adapter.
func (s *Server) fetchModelsFor(ctx context.Context, acc *pool.Account) ([]pool.ModelMetadata, error) {
	up, ok := s.upstreams.Get(acc.Type)
	if !ok {
		return nil, fmt.Errorf("no adapter registered for account type %q", acc.Type)
	}
	return up.FetchModels(ctx, acc)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	now := time.Now().Unix()
	var models []openai.ModelItem
	seen := make(map[string]bool)

	// 1. Collect dynamic models from all accounts
	for _, acc := range s.pool.List() {
		for _, m := range acc.GetModels() {
			if !seen[m.ID] {
				seen[m.ID] = true
				models = append(models, openai.ModelItem{
					ID:      m.ID,
					Object:  "model",
					Created: now,
					OwnedBy: string(acc.Type),
				})
			}
		}
	}

	// 2. Default models if no dynamic models yet
	if len(models) == 0 {
		for _, up := range s.upstreams.All() {
			for _, m := range up.DefaultModels() {
				if seen[m] {
					continue
				}
				seen[m] = true
				models = append(models, openai.ModelItem{
					ID:      m,
					Object:  "model",
					Created: now,
					OwnedBy: string(up.ID()),
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(openai.ModelListResponse{
		Object: "list",
		Data:   models,
	})
}

func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var req openai.ChatCompletionRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	rawModel := req.Model
	provider, upstreamModel := s.ResolveProvider(req.Model)
	req.Model = upstreamModel

	// Check and inject web search if requested
	s.maybeInjectWebSearch(&req)

	log.Printf("[OpenAI API] 收到客户端请求: 原始模型='%s' -> 路由至上游=[%s] (上游模型='%s', stream=%v)",
		rawModel, provider, upstreamModel, req.Stream)

	resp, up, err := s.callUpstream(r.Context(), provider, &req, bodyBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.forwardToOpenAI(w, up, resp, req.Model, req.Stream)
}

func (s *Server) maybeInjectWebSearch(req *openai.ChatCompletionRequest) {
	if req == nil || len(req.Messages) == 0 {
		return
	}

	lastMsg := req.Messages[len(req.Messages)-1]
	if lastMsg.Role != "user" {
		return
	}

	userText, ok := lastMsg.Content.(string)
	if !ok || len(userText) < 2 {
		return
	}

	searchKeywords := []string{
		"搜索", "搜一下", "搜搜", "查一下", "查查", "联网", "最新的", "今天", "今日", "实时",
		"search", "weather", "news", "current events", "latest",
	}

	wantsSearch := false
	lowerText := strings.ToLower(userText)
	for _, kw := range searchKeywords {
		if strings.Contains(lowerText, strings.ToLower(kw)) {
			wantsSearch = true
			break
		}
	}

	// Also check if any tool declared is web_search
	for _, t := range req.Tools {
		if t.Type == "web_search" || t.Type == "web_search_preview" || t.Function.Name == "web_search" {
			wantsSearch = true
			break
		}
	}

	if !wantsSearch {
		return
	}

	// Clean query for search
	query := cleanSearchQuery(userText)
	if query == "" {
		query = userText
	}

	log.Printf("[Web Search] 触发实时网络检索: 关键词='%s'", query)
	proxyURL := s.cfg.Server.Proxy
	results, err := webtools.Search(query, 5, proxyURL)
	if err != nil || len(results) == 0 {
		log.Printf("[Web Search] 检索未返回结果: %v", err)
		return
	}

	log.Printf("[Web Search] 检索成功，获取到 %d 条实时网页结果，正在注入模型上下文", len(results))
	formatted := webtools.FormatResultsAsMarkdown(query, results)

	searchSystemMsg := openai.ChatMessage{
		Role:    "system",
		Content: formatted + "\n请根据以上实时网络搜索结果，结合用户的提问进行准确、详实的解答，并在相关信息后引用来源链接。",
	}

	newMessages := make([]openai.ChatMessage, 0, len(req.Messages)+1)
	inserted := false
	for _, m := range req.Messages {
		if !inserted && m.Role == "user" {
			newMessages = append(newMessages, searchSystemMsg)
			inserted = true
		}
		newMessages = append(newMessages, m)
	}
	if !inserted {
		newMessages = append([]openai.ChatMessage{searchSystemMsg}, newMessages...)
	}
	req.Messages = newMessages
}

func cleanSearchQuery(raw string) string {
	q := strings.TrimSpace(raw)
	prefixes := []string{
		"请帮我搜索一下", "请帮我搜索", "请搜索一下", "请搜索", "帮我搜索一下", "帮我搜索", "搜索一下", "搜索",
		"请查一下", "请查询", "查一下", "查询", "搜搜", "搜一下", "搜",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(q, p) {
			q = strings.TrimPrefix(q, p)
			break
		}
	}

	// Normalize time words that trigger calendar sites in search engines
	q = strings.ReplaceAll(q, "今天最新的", "最新")
	q = strings.ReplaceAll(q, "今日最新的", "最新")
	q = strings.ReplaceAll(q, "今天最新", "最新")
	q = strings.ReplaceAll(q, "今日最新", "最新")

	fillers := []string{
		"，列出两条", "，列出两点", "，列出三条", "，列出3条", "，列出2条",
		"列出两条", "列出两点", "列出三条", "列出3条", "列出2条",
		"并列出两条", "并总结", "写一份简报", "详细介绍", "谢谢",
	}
	for _, f := range fillers {
		q = strings.ReplaceAll(q, f, "")
	}

	return strings.TrimSpace(q)
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var req anthropic.MessagesRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	rawModel := req.Model
	provider, upstreamModel := s.ResolveProvider(req.Model)
	req.Model = upstreamModel

	openAIReq, err := AnthropicToOpenAI(&req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.maybeInjectWebSearch(openAIReq)

	log.Printf("[Anthropic API] 收到客户端请求: 原始模型='%s' -> 路由至上游=[%s] (上游模型='%s', stream=%v, tools=%d)",
		rawModel, provider, upstreamModel, req.Stream, len(req.Tools))

	resp, up, err := s.callUpstream(r.Context(), provider, openAIReq, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.forwardToAnthropic(w, up, resp, openAIReq.Model, req.Stream)
}

func (s *Server) handleAnthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{
		"input_tokens": 100, // fast approximate
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"time":   time.Now().Format(time.RFC3339),
		"pool":   s.pool.Stats(),
	})
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	s.handleHealth(w, r)
}

type AccountDTO struct {
	ID         string               `json:"id"`
	Type       string               `json:"type"`
	Name       string               `json:"name"`
	Status     string               `json:"status"` // "healthy", "cooldown", "expired"
	FilePath   string               `json:"file_path,omitempty"`
	ExpiresAt  string               `json:"expires_at"`
	ProjectID  string               `json:"project_id,omitempty"`
	Email      string               `json:"email,omitempty"`
	UID        string               `json:"uid,omitempty"`
	Realm      string               `json:"realm,omitempty"`
	Product    string               `json:"product,omitempty"`
	Domain     string               `json:"domain,omitempty"`
	TokenMask  string               `json:"token_mask"`
	ModelCount int                  `json:"model_count"`
	Models     []pool.ModelMetadata `json:"models,omitempty"`
}

func (s *Server) handleAPIAccounts(w http.ResponseWriter, r *http.Request) {
	rawAccounts := s.pool.List()
	dtos := make([]AccountDTO, 0, len(rawAccounts))

	for _, acc := range rawAccounts {
		mask := "未设置"
		if len(acc.AccessToken) > 12 {
			mask = acc.AccessToken[:6] + "..." + acc.AccessToken[len(acc.AccessToken)-6:]
		}

		expiresStr := "永不过期 / 未知"
		if !acc.ExpiresAt.IsZero() {
			expiresStr = acc.ExpiresAt.Format("2006-01-02 15:04:05")
		}

		ms := acc.GetModels()
		dtos = append(dtos, AccountDTO{
			ID:         acc.ID,
			Type:       string(acc.Type),
			Name:       acc.Name,
			Status:     acc.Status(),
			FilePath:   acc.FilePath,
			ExpiresAt:  expiresStr,
			ProjectID:  acc.ProjectID,
			Email:      acc.Email,
			UID:        acc.UID,
			Realm:      acc.Realm,
			Product:    acc.Product,
			Domain:     acc.Domain,
			TokenMask:  mask,
			ModelCount: len(ms),
			Models:     ms,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(dtos)
}

type UIModelItem struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	DescriptionZh     string `json:"description_zh"`
	Credits           string `json:"credits,omitempty"`
	MaxInputTokens    int    `json:"max_input_tokens,omitempty"`
	SupportsImages    bool   `json:"supports_images,omitempty"`
	SupportsReasoning bool   `json:"supports_reasoning,omitempty"`
	SupportsToolCall  bool   `json:"supports_tool_call,omitempty"`
	Provider          string `json:"provider"`
	Active            bool   `json:"active"`
	Note              string `json:"note"`
}

func (s *Server) handleAPIModels(w http.ResponseWriter, r *http.Request) {
	healthy := make(map[pool.AccountType]bool)
	for _, up := range s.upstreams.All() {
		healthy[up.ID()] = s.pool.Count(up.ID()) > 0
	}

	seen := make(map[string]bool)
	var list []UIModelItem

	// 1. Gather dynamic models from accounts
	for _, acc := range s.pool.List() {
		for _, m := range acc.GetModels() {
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			displayName := m.Name
			if displayName == "" {
				displayName = m.ID
			}
			list = append(list, UIModelItem{
				ID:                m.ID,
				Name:              displayName,
				DescriptionZh:     m.DescriptionZh,
				Credits:           m.Credits,
				MaxInputTokens:    m.MaxInputTokens,
				SupportsImages:    m.SupportsImages,
				SupportsReasoning: m.SupportsReasoning,
				SupportsToolCall:  m.SupportsToolCall,
				Provider:          string(acc.Type),
				Active:            healthy[acc.Type],
				Note:              "上游账号实时拉取",
			})
		}
	}

	// 2. Static catalog for every registered provider, for models not yet covered
	for _, up := range s.upstreams.All() {
		for _, id := range up.DefaultModels() {
			if seen[id] {
				continue
			}
			seen[id] = true
			note := fmt.Sprintf("通道未就绪 (需添加 %s 凭据)", up.Label())
			if healthy[up.ID()] {
				note = "默认推荐模型"
			}
			list = append(list, UIModelItem{
				ID:                id,
				Name:              id,
				Provider:          string(up.ID()),
				Active:            healthy[up.ID()],
				Note:              note,
				SupportsToolCall:  true,
				SupportsReasoning: modelLooksReasoning(id),
				SupportsImages:    up.ID() == pool.TypeAntigravity,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

// modelLooksReasoning is a display hint for the dashboard only; actual
// capability comes from upstream metadata when an account reports it.
func modelLooksReasoning(id string) bool {
	lower := strings.ToLower(id)
	for _, hint := range []string{"reason", "thinking", "pro", "sonnet", "max", "kimi", "deepseek", "ultimate"} {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// handleAPISyncModels queries the upstream API to dynamically discover and cache its models.
func (s *Server) handleAPISyncModels(w http.ResponseWriter, r *http.Request) {
	accID := r.URL.Query().Get("id")
	var accounts []*pool.Account
	if accID != "" {
		acc := s.pool.Get(accID)
		if acc == nil {
			http.Error(w, "Account not found", http.StatusNotFound)
			return
		}
		accounts = append(accounts, acc)
	} else {
		accounts = s.pool.List()
	}

	type SyncResult struct {
		AccountID string               `json:"account_id"`
		Name      string               `json:"name"`
		Type      string               `json:"type"`
		Count     int                  `json:"count"`
		Models    []pool.ModelMetadata `json:"models"`
		Error     string               `json:"error,omitempty"`
	}

	var results []SyncResult
	for _, acc := range accounts {
		ms, err := s.fetchModelsFor(r.Context(), acc)

		res := SyncResult{
			AccountID: acc.ID,
			Name:      acc.Name,
			Type:      string(acc.Type),
			Count:     len(ms),
			Models:    ms,
		}
		if err != nil {
			res.Error = err.Error()
			log.Printf("[SyncModels] Account %s sync error: %v", acc.Name, err)
		} else {
			s.pool.SetAccountModels(acc.ID, ms)
			log.Printf("[SyncModels] Account %s successfully fetched %d models from upstream API", acc.Name, len(ms))
			if acc.FilePath != "" {
				_ = pool.SaveAccountToFile(acc)
			}
		}
		results = append(results, res)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"results": results,
	})
}

// handleAPIImportAccount allows directly importing an account via Web UI and immediately discovering its models.
func (s *Server) handleAPIImportAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Read request body failed", http.StatusBadRequest)
		return
	}

	// Try parsing generic JSON
	var raw pool.GenericOAuthFile
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	id := raw.Email
	if id == "" {
		id = raw.UID
	}
	if id == "" {
		id = raw.UserIDCamel
	}
	if id == "" {
		id = fmt.Sprintf("account_%x", time.Now().Unix())
	}

	safeID := strings.NewReplacer("@", "-", ":", "-", "/", "-", "\\", "-", " ", "-").Replace(id)
	oauthDir := "./data/oauth"
	_ = os.MkdirAll(oauthDir, 0755)
	filePath := filepath.Join(oauthDir, safeID+".json")

	if err := os.WriteFile(filePath, bodyBytes, 0644); err != nil {
		http.Error(w, fmt.Sprintf("Save file failed: %v", err), http.StatusInternalServerError)
		return
	}

	acc, err := pool.ParseAccountData(filePath, bodyBytes)
	if err != nil {
		http.Error(w, fmt.Sprintf("Parse account failed: %v", err), http.StatusBadRequest)
		return
	}

	s.pool.AddOrUpdate(acc)

	// Immediately fetch models from upstream API
	ms, fetchErr := s.fetchModelsFor(r.Context(), acc)

	fetchErrMsg := ""
	if fetchErr != nil {
		fetchErrMsg = fetchErr.Error()
	} else if len(ms) > 0 {
		s.pool.SetAccountModels(acc.ID, ms)
		_ = pool.SaveAccountToFile(acc)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":       "ok",
		"account_id":   acc.ID,
		"type":         acc.Type,
		"name":         acc.Name,
		"models_count": len(ms),
		"models":       ms,
		"fetch_error":  fetchErrMsg,
	})
}

func (s *Server) handleAPIResetCooldown(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id != "" {
		s.pool.ResetCooldown(id)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) aggregateOpenAISSEToJSON(w http.ResponseWriter, src io.Reader, model string) {
	var fullText strings.Builder
	var fullReasoning strings.Builder

	buf := make([]byte, 4096)
	var rawBuffer bytes.Buffer
	for {
		n, err := src.Read(buf)
		if n > 0 {
			rawBuffer.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}

	lines := strings.Split(rawBuffer.String(), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err == nil && len(chunk.Choices) > 0 {
			fullText.WriteString(chunk.Choices[0].Delta.Content)
			fullReasoning.WriteString(chunk.Choices[0].Delta.ReasoningContent)
		}
	}

	resp := openai.ChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-%x", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openai.Choice{
			{
				Index: 0,
				Message: openai.ChatMessage{
					Role:             "assistant",
					Content:          fullText.String(),
					ReasoningContent: fullReasoning.String(),
				},
				FinishReason: "stop",
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) aggregateGeminiSSEToOpenAI(w http.ResponseWriter, src io.Reader, model string) {
	var fullText strings.Builder
	var fullReasoning strings.Builder

	buf := make([]byte, 4096)
	var rawBuffer bytes.Buffer
	for {
		n, err := src.Read(buf)
		if n > 0 {
			rawBuffer.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}

	lines := strings.Split(rawBuffer.String(), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var geminiResp struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text    string `json:"text"`
						Thought bool   `json:"thought"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}

		if err := json.Unmarshal([]byte(payload), &geminiResp); err == nil && len(geminiResp.Candidates) > 0 {
			for _, part := range geminiResp.Candidates[0].Content.Parts {
				if part.Thought {
					fullReasoning.WriteString(part.Text)
				} else {
					fullText.WriteString(part.Text)
				}
			}
		}
	}

	resp := openai.ChatCompletionResponse{
		ID:      fmt.Sprintf("chatcmpl-%x", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openai.Choice{
			{
				Index: 0,
				Message: openai.ChatMessage{
					Role:             "assistant",
					Content:          fullText.String(),
					ReasoningContent: fullReasoning.String(),
				},
				FinishReason: "stop",
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) aggregateGeminiSSEToAnthropic(w http.ResponseWriter, src io.Reader, model string) {
	var fullText strings.Builder
	var blocks []anthropic.ContentBlock
	hasTool := false

	buf := make([]byte, 4096)
	var rawBuffer bytes.Buffer
	for {
		n, err := src.Read(buf)
		if n > 0 {
			rawBuffer.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}

	lines := strings.Split(rawBuffer.String(), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var geminiResp struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text         string `json:"text"`
						Thought      bool   `json:"thought"`
						FunctionCall *struct {
							Name string                 `json:"name"`
							Args map[string]interface{} `json:"args"`
						} `json:"functionCall"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}

		if err := json.Unmarshal([]byte(payload), &geminiResp); err == nil && len(geminiResp.Candidates) > 0 {
			for i, part := range geminiResp.Candidates[0].Content.Parts {
				if part.FunctionCall != nil {
					hasTool = true
					blocks = append(blocks, anthropic.ContentBlock{
						Type:  "tool_use",
						ID:    fmt.Sprintf("call_%d_%s", i, RandID()),
						Name:  part.FunctionCall.Name,
						Input: part.FunctionCall.Args,
					})
				} else if !part.Thought {
					fullText.WriteString(part.Text)
				}
			}
		}
	}

	if fullText.Len() > 0 {
		blocks = append([]anthropic.ContentBlock{{Type: "text", Text: fullText.String()}}, blocks...)
	}

	stopReason := "end_turn"
	if hasTool {
		stopReason = "tool_use"
	}

	resp := anthropic.MessagesResponse{
		ID:         fmt.Sprintf("msg_%x", time.Now().UnixNano()),
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		Content:    blocks,
		StopReason: stopReason,
		Usage: anthropic.Usage{
			InputTokens:  10,
			OutputTokens: len(fullText.String()) / 4,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) aggregateOpenAISSEToAnthropic(w http.ResponseWriter, src io.Reader, model string) {
	var fullText strings.Builder
	buf := make([]byte, 4096)
	var rawBuffer bytes.Buffer
	for {
		n, err := src.Read(buf)
		if n > 0 {
			rawBuffer.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}

	lines := strings.Split(rawBuffer.String(), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err == nil && len(chunk.Choices) > 0 {
			fullText.WriteString(chunk.Choices[0].Delta.Content)
		}
	}

	resp := anthropic.MessagesResponse{
		ID:    fmt.Sprintf("msg_%x", time.Now().UnixNano()),
		Type:  "message",
		Role:  "assistant",
		Model: model,
		Content: []anthropic.ContentBlock{
			{
				Type: "text",
				Text: fullText.String(),
			},
		},
		StopReason: "end_turn",
		Usage: anthropic.Usage{
			InputTokens:  10,
			OutputTokens: len(fullText.String()) / 4,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// streamCaptureWriter adapts an io.Writer as an http.ResponseWriter
type streamCaptureWriter struct {
	w      io.Writer
	header http.Header
}

func (s *streamCaptureWriter) Header() http.Header {
	if s.header == nil {
		s.header = make(http.Header)
	}
	return s.header
}

func (s *streamCaptureWriter) Write(p []byte) (int, error) {
	return s.w.Write(p)
}

func (s *streamCaptureWriter) WriteHeader(statusCode int) {}

func (s *streamCaptureWriter) Flush() {}
