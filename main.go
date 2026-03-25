package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type AdminAccount struct {
	ID           uint   `gorm:"primaryKey"`
	Username     string `gorm:"uniqueIndex;size:64"`
	PasswordHash string `gorm:"size:255"`
	CreatedAt    int64
	UpdatedAt    int64
}

type AppConfig struct {
	ID uint `gorm:"primaryKey"`

	MainBaseURL  string
	MainUsername string
	MainPassword string
	MainDBPath   string

	BotEnabled  bool
	BotToken    string
	BotAdminIDs string
	PollSec     int

	QuotaPer100 int64

	CreatedAt int64
	UpdatedAt int64
}

type Server struct {
	db *gorm.DB

	clientMu     sync.Mutex
	mainClient   *http.Client
	mainBaseURL  string
	sessionAlive bool

	tgOffsetMu sync.Mutex
	tgOffset   int

	commandMu sync.Mutex
	cmdToken  string
	cmdAt     int64
}

type TelegramUpdateResp struct {
	OK     bool             `json:"ok"`
	Result []TelegramUpdate `json:"result"`
}

type TelegramUpdate struct {
	UpdateID      int               `json:"update_id"`
	Message       *TelegramMessage  `json:"message"`
	CallbackQuery *TelegramCallback `json:"callback_query"`
}

type TelegramMessage struct {
	MessageID int           `json:"message_id"`
	Text      string        `json:"text"`
	Chat      TelegramChat  `json:"chat"`
	From      *TelegramUser `json:"from"`
}

type TelegramCallback struct {
	ID      string           `json:"id"`
	From    *TelegramUser    `json:"from"`
	Message *TelegramMessage `json:"message"`
	Data    string           `json:"data"`
}

type TelegramChat struct {
	ID int64 `json:"id"`
}

type TelegramUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

func main() {
	dataDir := getenvDefault("DATA_DIR", "./data")
	port := getenvDefault("PORT", "8088")

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatal(err)
	}

	dbPath := filepath.Join(dataDir, "manager.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}
	if err := db.AutoMigrate(&AdminAccount{}, &AppConfig{}); err != nil {
		log.Fatal(err)
	}
	if err := ensureDefaults(db); err != nil {
		log.Fatal(err)
	}

	s := &Server{db: db}

	r := gin.Default()
	store := cookie.NewStore([]byte(getenvDefault("SESSION_SECRET", "api-web-tgbot-change-this-secret")))
	store.Options(sessions.Options{Path: "/", MaxAge: 86400 * 7, HttpOnly: true})
	r.Use(sessions.Sessions("awt_session", store))

	r.GET("/login", s.getLoginPage)
	r.POST("/api/login", s.apiLogin)
	r.GET("/logout", s.logout)

	authed := r.Group("/")
	authed.Use(s.authRequired)
	{
		authed.GET("/", s.getIndexPage)
		authed.GET("/api/me", s.apiMe)
		authed.POST("/api/logout", s.apiLogout)
		authed.POST("/api/account", s.apiUpdateAccount)
		authed.GET("/api/config", s.apiGetConfig)
		authed.POST("/api/config", s.apiSaveConfig)
		authed.GET("/api/health/main", s.apiMainHealth)
		authed.GET("/api/db/search", s.apiSearchDBPaths)

		authed.GET("/api/channels", s.apiGetChannels)
		authed.POST("/api/channels", s.apiCreateChannel)
		authed.PUT("/api/channels/:id", s.apiUpdateChannel)
		authed.DELETE("/api/channels/:id", s.apiDeleteChannel)
	}

	go s.telegramLoop()

	log.Printf("api-web-tgbot started at :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

func ensureDefaults(db *gorm.DB) error {
	var cnt int64
	if err := db.Model(&AdminAccount{}).Count(&cnt).Error; err != nil {
		return err
	}
	if cnt == 0 {
		hash, _ := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
		now := time.Now().Unix()
		acc := AdminAccount{Username: "admin", PasswordHash: string(hash), CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&acc).Error; err != nil {
			return err
		}
	}

	var cfg AppConfig
	err := db.First(&cfg, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		now := time.Now().Unix()
		cfg = AppConfig{
			ID:          1,
			MainBaseURL: "http://127.0.0.1:3000",
			BotEnabled:  false,
			PollSec:     5,
			QuotaPer100: 50000000,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		return db.Create(&cfg).Error
	}
	return err
}

func (s *Server) authRequired(c *gin.Context) {
	sess := sessions.Default(c)
	if sess.Get("uid") == nil {
		c.Redirect(http.StatusFound, "/login")
		c.Abort()
		return
	}
	c.Next()
}

func (s *Server) getLoginPage(c *gin.Context) {
	if sessions.Default(c).Get("uid") != nil {
		c.Redirect(http.StatusFound, "/")
		return
	}
	c.File("web/login.html")
}

func (s *Server) getIndexPage(c *gin.Context) {
	c.File("web/index.html")
}

func (s *Server) apiLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "参数错误"})
		return
	}
	var acc AdminAccount
	if err := s.db.Where("username = ?", strings.TrimSpace(req.Username)).First(&acc).Error; err != nil {
		c.JSON(401, gin.H{"success": false, "message": "账号或密码错误"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(acc.PasswordHash), []byte(req.Password)) != nil {
		c.JSON(401, gin.H{"success": false, "message": "账号或密码错误"})
		return
	}
	sess := sessions.Default(c)
	sess.Set("uid", acc.ID)
	sess.Set("username", acc.Username)
	_ = sess.Save()
	c.JSON(200, gin.H{"success": true})
}

func (s *Server) logout(c *gin.Context) {
	sess := sessions.Default(c)
	sess.Clear()
	_ = sess.Save()
	c.Redirect(http.StatusFound, "/login")
}

func (s *Server) apiLogout(c *gin.Context) {
	sess := sessions.Default(c)
	sess.Clear()
	_ = sess.Save()
	c.JSON(200, gin.H{"success": true})
}

func (s *Server) apiMe(c *gin.Context) {
	c.JSON(200, gin.H{"success": true, "data": gin.H{"username": sessions.Default(c).Get("username")}})
}

func (s *Server) apiUpdateAccount(c *gin.Context) {
	var req struct {
		Username    string `json:"username"`
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "参数错误"})
		return
	}

	uid := sessions.Default(c).Get("uid")
	if uid == nil {
		c.JSON(401, gin.H{"success": false, "message": "未登录"})
		return
	}

	var acc AdminAccount
	if err := s.db.First(&acc, uid).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "message": err.Error()})
		return
	}

	if strings.TrimSpace(req.Username) != "" {
		acc.Username = strings.TrimSpace(req.Username)
	}
	if strings.TrimSpace(req.NewPassword) != "" {
		if bcrypt.CompareHashAndPassword([]byte(acc.PasswordHash), []byte(req.OldPassword)) != nil {
			c.JSON(400, gin.H{"success": false, "message": "旧密码错误"})
			return
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
		acc.PasswordHash = string(hash)
	}

	acc.UpdatedAt = time.Now().Unix()
	if err := s.db.Save(&acc).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "message": err.Error()})
		return
	}

	sess := sessions.Default(c)
	sess.Set("username", acc.Username)
	_ = sess.Save()
	c.JSON(200, gin.H{"success": true})
}

func (s *Server) getConfig() (AppConfig, error) {
	var cfg AppConfig
	err := s.db.First(&cfg, 1).Error
	return cfg, err
}

func (s *Server) apiGetConfig(c *gin.Context) {
	cfg, err := s.getConfig()
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": err.Error()})
		return
	}
	cfg.MainPassword = maskSecret(cfg.MainPassword)
	cfg.BotToken = maskSecret(cfg.BotToken)
	c.JSON(200, gin.H{"success": true, "data": cfg})
}

func (s *Server) apiSaveConfig(c *gin.Context) {
	var req AppConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "参数错误"})
		return
	}

	cfg, err := s.getConfig()
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": err.Error()})
		return
	}

	cfg.MainBaseURL = strings.TrimSpace(req.MainBaseURL)
	cfg.MainUsername = strings.TrimSpace(req.MainUsername)
	cfg.MainDBPath = strings.TrimSpace(req.MainDBPath)
	if req.MainPassword != "" && !strings.HasPrefix(req.MainPassword, "***") {
		cfg.MainPassword = req.MainPassword
	}

	cfg.BotEnabled = req.BotEnabled
	if req.BotToken != "" && !strings.HasPrefix(req.BotToken, "***") {
		cfg.BotToken = req.BotToken
	}
	cfg.BotAdminIDs = normalizeAdminIDs(req.BotAdminIDs)
	if req.PollSec < 3 {
		req.PollSec = 5
	}
	cfg.PollSec = req.PollSec
	if req.QuotaPer100 <= 0 {
		req.QuotaPer100 = 50000000
	}
	cfg.QuotaPer100 = req.QuotaPer100
	cfg.UpdatedAt = time.Now().Unix()

	if err := s.db.Save(&cfg).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{"success": true})
}

func (s *Server) apiMainHealth(c *gin.Context) {
	_, err := s.mainAPI(http.MethodGet, "/api/status", nil)
	if err != nil {
		c.JSON(200, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"success": true})
}

func (s *Server) apiSearchDBPaths(c *gin.Context) {
	roots := []string{"/data", "/opt", "/root", "/home", "/var/lib", "/Users"}
	cwd, _ := os.Getwd()
	if cwd != "" {
		roots = append(roots, cwd)
	}
	seen := map[string]bool{}
	result := make([]string, 0, 32)

	looksLikeDB := func(name string) bool {
		name = strings.ToLower(name)
		return strings.HasSuffix(name, ".db") ||
			strings.HasSuffix(name, ".sqlite") ||
			strings.Contains(name, "one-api.db") ||
			strings.Contains(name, "new-api.db")
	}

	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			// Skip very heavy/system folders to keep the scan quick.
			if d.IsDir() {
				base := strings.ToLower(d.Name())
				if base == ".git" || base == "node_modules" || base == "proc" || base == "sys" || base == "dev" || base == "tmp" {
					return filepath.SkipDir
				}
			}
			if d.IsDir() {
				return nil
			}
			if looksLikeDB(d.Name()) {
				if !seen[path] {
					seen[path] = true
					result = append(result, path)
				}
			}
			if len(result) >= 50 {
				return filepath.SkipDir
			}
			return nil
		})
	}

	sort.Strings(result)
	c.JSON(200, gin.H{"success": true, "data": result})
}

func (s *Server) getMainClient(cfg AppConfig) (*http.Client, error) {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()

	if s.mainClient == nil || s.mainBaseURL != cfg.MainBaseURL {
		jar, _ := cookiejar.New(nil)
		s.mainClient = &http.Client{Timeout: 30 * time.Second, Jar: jar}
		s.mainBaseURL = cfg.MainBaseURL
		s.sessionAlive = false
	}

	if !s.sessionAlive {
		payload := map[string]string{"username": cfg.MainUsername, "password": cfg.MainPassword}
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.MainBaseURL, "/")+"/api/user/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.mainClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("主程序登录失败: %s", string(raw))
		}
		if !isSuccess(raw) {
			return nil, fmt.Errorf("主程序登录失败: %s", compactMessage(raw))
		}
		s.sessionAlive = true
	}

	return s.mainClient, nil
}

func (s *Server) mainAPI(method, path string, body any) ([]byte, error) {
	cfg, err := s.getConfig()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.MainBaseURL) == "" || strings.TrimSpace(cfg.MainUsername) == "" || strings.TrimSpace(cfg.MainPassword) == "" {
		return nil, fmt.Errorf("请先配置主程序连接")
	}

	client, err := s.getMainClient(cfg)
	if err != nil {
		return nil, err
	}

	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	url := strings.TrimRight(cfg.MainBaseURL, "/") + path
	req, _ := http.NewRequestWithContext(context.Background(), method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		s.clientMu.Lock()
		s.sessionAlive = false
		s.clientMu.Unlock()
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("主程序请求失败[%d]: %s", resp.StatusCode, string(raw))
	}
	if !isSuccess(raw) {
		return nil, fmt.Errorf("主程序返回失败: %s", compactMessage(raw))
	}
	return raw, nil
}

func (s *Server) apiGetChannels(c *gin.Context) {
	raw, err := s.mainAPI(http.MethodGet, "/api/channel/?p=0&page_size=100&id_sort=true&tag_mode=true", nil)
	if err != nil {
		c.JSON(200, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.Data(200, "application/json", raw)
}

func (s *Server) apiCreateChannel(c *gin.Context) {
	var payload map[string]any
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "参数错误"})
		return
	}
	raw, err := s.mainAPI(http.MethodPost, "/api/channel/", payload)
	if err != nil {
		c.JSON(200, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.Data(200, "application/json", raw)
}

func (s *Server) apiUpdateChannel(c *gin.Context) {
	var payload map[string]any
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "参数错误"})
		return
	}
	id, _ := strconv.Atoi(c.Param("id"))
	payload["id"] = id
	raw, err := s.mainAPI(http.MethodPut, "/api/channel/", payload)
	if err != nil {
		c.JSON(200, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.Data(200, "application/json", raw)
}

func (s *Server) apiDeleteChannel(c *gin.Context) {
	id := c.Param("id")
	raw, err := s.mainAPI(http.MethodDelete, "/api/channel/"+id, nil)
	if err != nil {
		c.JSON(200, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.Data(200, "application/json", raw)
}

func (s *Server) telegramLoop() {
	for {
		cfg, err := s.getConfig()
		if err != nil {
			log.Printf("telegram: get config failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if !cfg.BotEnabled || strings.TrimSpace(cfg.BotToken) == "" {
			time.Sleep(5 * time.Second)
			continue
		}

		s.syncCommands(cfg.BotToken)
		if err := s.pollTelegram(cfg); err != nil {
			log.Printf("telegram: poll failed: %v", err)
			time.Sleep(3 * time.Second)
		}

		if cfg.PollSec <= 0 {
			cfg.PollSec = 5
		}
		time.Sleep(time.Duration(cfg.PollSec) * time.Second)
	}
}

func (s *Server) syncCommands(token string) {
	s.commandMu.Lock()
	if s.cmdToken == token && time.Now().Unix()-s.cmdAt < 3600 {
		s.commandMu.Unlock()
		return
	}
	s.cmdToken = token
	s.cmdAt = time.Now().Unix()
	s.commandMu.Unlock()

	commands := map[string]any{
		"commands": []map[string]string{
			{"command": "start", "description": "打开管理菜单"},
			{"command": "help", "description": "查看命令帮助"},
			{"command": "stats", "description": "使用统计和性能指标"},
			{"command": "users", "description": "用户交互管理"},
			{"command": "redeem", "description": "交互生成兑换码"},
		},
	}
	_, _ = tgCall(token, "setMyCommands", commands)
}

func (s *Server) pollTelegram(cfg AppConfig) error {
	s.tgOffsetMu.Lock()
	offset := s.tgOffset
	s.tgOffsetMu.Unlock()

	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?timeout=25&offset=%d", cfg.BotToken, offset)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("getUpdates failed: %s", string(raw))
	}

	var updates TelegramUpdateResp
	if err := json.NewDecoder(resp.Body).Decode(&updates); err != nil {
		return err
	}

	for _, up := range updates.Result {
		if up.UpdateID >= offset {
			offset = up.UpdateID + 1
		}
		if up.Message != nil {
			s.handleTGMessage(cfg, up.Message)
		}
		if up.CallbackQuery != nil {
			s.handleTGCallback(cfg, up.CallbackQuery)
		}
	}

	s.tgOffsetMu.Lock()
	s.tgOffset = offset
	s.tgOffsetMu.Unlock()

	return nil
}

func (s *Server) isTGAdmin(cfg AppConfig, uid int64) bool {
	ids := splitAdminIDs(cfg.BotAdminIDs)
	target := strconv.FormatInt(uid, 10)
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func (s *Server) handleTGMessage(cfg AppConfig, msg *TelegramMessage) {
	if msg == nil || msg.From == nil || strings.TrimSpace(msg.Text) == "" {
		return
	}
	if !s.isTGAdmin(cfg, msg.From.ID) {
		return
	}

	text, markup := s.executeTGCommand(strings.TrimSpace(msg.Text))
	_, _ = tgSend(cfg.BotToken, strconv.FormatInt(msg.Chat.ID, 10), text, markup)
}

func (s *Server) handleTGCallback(cfg AppConfig, cb *TelegramCallback) {
	if cb == nil || cb.From == nil {
		return
	}
	if !s.isTGAdmin(cfg, cb.From.ID) {
		_, _ = tgCall(cfg.BotToken, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": "无权限"})
		return
	}

	text, markup, ack := s.executeTGCallback(cb.Data)
	if cb.Message != nil && text != "" {
		_, _ = tgSend(cfg.BotToken, strconv.FormatInt(cb.Message.Chat.ID, 10), text, markup)
	}
	if ack == "" {
		ack = "已处理"
	}
	_, _ = tgCall(cfg.BotToken, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": ack})
}

func (s *Server) executeTGCommand(text string) (string, map[string]any) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return s.helpText(), nil
	}
	cmd := strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])
	switch cmd {
	case "/start", "/help":
		return s.helpText(), nil
	case "/stats":
		return s.tgStats(), nil
	case "/users":
		return s.tgUsersMenu()
	case "/redeem":
		return s.tgRedeemAmountMenu()
	default:
		return s.helpText(), nil
	}
}

func (s *Server) helpText() string {
	return strings.Join([]string{
		"API-WEB-TGBOT 命令：",
		"/stats 详细统计（请求、次数、额度、Tokens、RPM）",
		"/users 用户概览 + 二级交互菜单",
		"/redeem 交互生成兑换码",
	}, "\n")
}

func (s *Server) tgStats() string {
	statusRaw, err := s.mainAPI(http.MethodGet, "/api/status", nil)
	if err != nil {
		return "统计失败: " + err.Error()
	}
	quotaPerUnit := getInt64FromPath(statusRaw, "data", "quota_per_unit")
	if quotaPerUnit <= 0 {
		quotaPerUnit = 500000
	}

	now := time.Now().Unix()
	since := now - 86400

	logStatRaw, err := s.mainAPI(http.MethodGet, fmt.Sprintf("/api/log/stat?type=2&start_timestamp=%d&end_timestamp=%d", since, now), nil)
	if err != nil {
		return "统计失败: " + err.Error()
	}

	rpmRealtime := getInt64FromPath(logStatRaw, "data", "rpm")
	tpmRealtime := getInt64FromPath(logStatRaw, "data", "tpm")
	quota24Raw := getInt64FromPath(logStatRaw, "data", "quota")

	usersRaw, err := s.mainAPI(http.MethodGet, "/api/user/?p=0&page_size=1000", nil)
	if err != nil {
		return "统计失败: " + err.Error()
	}
	users := getItems(usersRaw)
	totalUsers := len(users)
	enabledUsers := 0
	var requestTotal int64
	for _, u := range users {
		if int(getMapNumber(u, "status")) == 1 {
			enabledUsers++
		}
		requestTotal += int64(getMapNumber(u, "request_count"))
	}

	logsRaw, err := s.mainAPI(http.MethodGet, fmt.Sprintf("/api/log/?type=2&p=0&page_size=1000&start_timestamp=%d&end_timestamp=%d", since, now), nil)
	if err != nil {
		return "统计失败: " + err.Error()
	}
	logTotal := getTotal(logsRaw)
	logItems := getItems(logsRaw)
	var tokens int64
	for _, item := range logItems {
		tokens += int64(getMapNumber(item, "prompt_tokens") + getMapNumber(item, "completion_tokens"))
	}

	avgRPM := 0.0
	if logTotal > 0 {
		avgRPM = float64(logTotal) / 1440.0
	}

	return strings.Join([]string{
		"系统统计（最近24小时）",
		fmt.Sprintf("用户总数: %d", totalUsers),
		fmt.Sprintf("启用用户: %d", enabledUsers),
		fmt.Sprintf("请求次数(累计): %d", requestTotal),
		fmt.Sprintf("统计次数(24h): %d", logTotal),
		fmt.Sprintf("统计额度(24h): %.2f", float64(quota24Raw)/float64(quotaPerUnit)),
		fmt.Sprintf("统计Tokens(24h样本): %d", tokens),
		fmt.Sprintf("平均RPM(24h): %.2f", avgRPM),
		fmt.Sprintf("实时RPM(60s): %d", rpmRealtime),
		fmt.Sprintf("实时TPM(60s): %d", tpmRealtime),
	}, "\n")
}

func (s *Server) tgUsersMenu() (string, map[string]any) {
	raw, err := s.mainAPI(http.MethodGet, "/api/user/?p=0&page_size=1000", nil)
	if err != nil {
		return "读取用户失败: " + err.Error(), nil
	}

	users := getItems(raw)
	sort.Slice(users, func(i, j int) bool {
		return getMapNumber(users[i], "quota") < getMapNumber(users[j], "quota")
	})
	if len(users) > 10 {
		users = users[:10]
	}

	rows := make([][]map[string]string, 0, len(users))
	lines := []string{"用户概览（额度最低前10）", "点击用户进入二级菜单："}
	for _, u := range users {
		uid := int(getMapNumber(u, "id"))
		username := getMapString(u, "username")
		quota := int64(getMapNumber(u, "quota"))
		lines = append(lines, fmt.Sprintf("ID:%d | %s | 余额: %.2f", uid, username, s.quotaRawToDisplay(quota)))
		rows = append(rows, []map[string]string{{
			"text":          fmt.Sprintf("%s(%d)", username, uid),
			"callback_data": fmt.Sprintf("u:view:%d", uid),
		}})
	}

	return strings.Join(lines, "\n"), map[string]any{"inline_keyboard": rows}
}

func (s *Server) tgRedeemAmountMenu() (string, map[string]any) {
	amounts := []int{20, 50, 100, 150, 200}
	rows := make([][]map[string]string, 0)
	line := make([]map[string]string, 0, 3)
	for _, amount := range amounts {
		line = append(line, map[string]string{"text": fmt.Sprintf("%d", amount), "callback_data": fmt.Sprintf("r:amt:%d", amount)})
		if len(line) == 3 {
			rows = append(rows, line)
			line = make([]map[string]string, 0, 3)
		}
	}
	if len(line) > 0 {
		rows = append(rows, line)
	}

	return "兑换码菜单：先选择金额", map[string]any{"inline_keyboard": rows}
}

func (s *Server) executeTGCallback(data string) (string, map[string]any, string) {
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return "无效操作", nil, "无效操作"
	}

	if parts[0] == "u" {
		switch parts[1] {
		case "view":
			if len(parts) < 3 {
				return "参数错误", nil, "参数错误"
			}
			uid, _ := strconv.Atoi(parts[2])
			return s.userActionMenu(uid)
		case "act":
			if len(parts) < 4 {
				return "参数错误", nil, "参数错误"
			}
			uid, _ := strconv.Atoi(parts[2])
			op := parts[3]
			return s.userAmountMenu(uid, op)
		case "amt":
			if len(parts) < 5 {
				return "参数错误", nil, "参数错误"
			}
			uid, _ := strconv.Atoi(parts[2])
			op := parts[3]
			amount, _ := strconv.Atoi(parts[4])
			msg := s.applyUserQuota(uid, op == "add", amount)
			text, markup, _ := s.userActionMenu(uid)
			return msg + "\n\n" + text, markup, "额度已更新"
		case "set":
			if len(parts) < 4 {
				return "参数错误", nil, "参数错误"
			}
			uid, _ := strconv.Atoi(parts[2])
			enable := parts[3] == "enable"
			msg := s.applyUserStatus(uid, enable)
			text, markup, _ := s.userActionMenu(uid)
			return msg + "\n\n" + text, markup, "状态已更新"
		}
	}

	if parts[0] == "r" {
		switch parts[1] {
		case "amt":
			if len(parts) < 3 {
				return "参数错误", nil, "参数错误"
			}
			amount, _ := strconv.Atoi(parts[2])
			return s.redeemCountMenu(amount)
		case "cnt":
			if len(parts) < 4 {
				return "参数错误", nil, "参数错误"
			}
			amount, _ := strconv.Atoi(parts[2])
			count, _ := strconv.Atoi(parts[3])
			msg := s.createRedeem(amount, count)
			text, markup := s.tgRedeemAmountMenu()
			return msg + "\n\n" + text, markup, "生成完成"
		}
	}

	return "未识别操作", nil, "未识别"
}

func (s *Server) userActionMenu(uid int) (string, map[string]any, string) {
	raw, err := s.mainAPI(http.MethodGet, fmt.Sprintf("/api/user/%d", uid), nil)
	if err != nil {
		return "读取用户失败: " + err.Error(), nil, "读取失败"
	}
	u := getDataMap(raw)
	quotaRaw := int64(getMapNumber(u, "quota"))
	status := int(getMapNumber(u, "status"))
	statusText := "停用"
	if status == 1 {
		statusText = "启用"
	}

	text := strings.Join([]string{
		fmt.Sprintf("用户: %s (ID:%d)", getMapString(u, "username"), uid),
		fmt.Sprintf("余额: %.2f", s.quotaRawToDisplay(quotaRaw)),
		fmt.Sprintf("状态: %s", statusText),
		"请选择操作：",
	}, "\n")

	markup := map[string]any{"inline_keyboard": [][]map[string]string{
		{{"text": "增加额度", "callback_data": fmt.Sprintf("u:act:%d:add", uid)}, {"text": "减少额度", "callback_data": fmt.Sprintf("u:act:%d:sub", uid)}},
		{{"text": "启用账户", "callback_data": fmt.Sprintf("u:set:%d:enable", uid)}, {"text": "停用账户", "callback_data": fmt.Sprintf("u:set:%d:disable", uid)}},
	}}

	return text, markup, "已打开"
}

func (s *Server) userAmountMenu(uid int, op string) (string, map[string]any, string) {
	amounts := []int{10, 30, 50, 100, 150, 200, 500}
	rows := make([][]map[string]string, 0)
	line := make([]map[string]string, 0, 3)
	for _, amount := range amounts {
		line = append(line, map[string]string{
			"text":          fmt.Sprintf("%d", amount),
			"callback_data": fmt.Sprintf("u:amt:%d:%s:%d", uid, op, amount),
		})
		if len(line) == 3 {
			rows = append(rows, line)
			line = make([]map[string]string, 0, 3)
		}
	}
	if len(line) > 0 {
		rows = append(rows, line)
	}
	return "请选择额度档位", map[string]any{"inline_keyboard": rows}, "请选择"
}

func (s *Server) applyUserStatus(uid int, enable bool) string {
	action := "disable"
	if enable {
		action = "enable"
	}
	_, err := s.mainAPI(http.MethodPost, "/api/user/manage", map[string]any{"id": uid, "action": action})
	if err != nil {
		return "状态更新失败: " + err.Error()
	}
	if enable {
		return "已启用账户"
	}
	return "已停用账户"
}

func (s *Server) applyUserQuota(uid int, increase bool, amount int) string {
	raw, err := s.mainAPI(http.MethodGet, fmt.Sprintf("/api/user/%d", uid), nil)
	if err != nil {
		return "读取用户失败: " + err.Error()
	}
	u := getDataMap(raw)

	oldQuota := int64(getMapNumber(u, "quota"))
	delta := s.displayAmountToRaw(amount)
	newQuota := oldQuota + delta
	verb := "增加"
	if !increase {
		verb = "减少"
		newQuota = oldQuota - delta
		if newQuota < 0 {
			newQuota = 0
		}
	}

	payload := map[string]any{
		"id":           int(getMapNumber(u, "id")),
		"username":     getMapString(u, "username"),
		"display_name": getMapString(u, "display_name"),
		"role":         int(getMapNumber(u, "role")),
		"group":        getMapString(u, "group"),
		"quota":        newQuota,
		"remark":       getMapString(u, "remark"),
	}
	_, err = s.mainAPI(http.MethodPut, "/api/user/", payload)
	if err != nil {
		return "额度更新失败: " + err.Error()
	}

	return fmt.Sprintf("已%s %d，现有 %.2f", verb, amount, s.quotaRawToDisplay(newQuota))
}

func (s *Server) redeemCountMenu(amount int) (string, map[string]any, string) {
	counts := []int{1, 5, 10, 20, 30, 50, 100}
	rows := make([][]map[string]string, 0)
	line := make([]map[string]string, 0, 3)
	for _, count := range counts {
		line = append(line, map[string]string{
			"text":          fmt.Sprintf("%d", count),
			"callback_data": fmt.Sprintf("r:cnt:%d:%d", amount, count),
		})
		if len(line) == 3 {
			rows = append(rows, line)
			line = make([]map[string]string, 0, 3)
		}
	}
	if len(line) > 0 {
		rows = append(rows, line)
	}

	return fmt.Sprintf("已选金额 %d，请选择生成几张", amount), map[string]any{"inline_keyboard": rows}, "请选择张数"
}

func (s *Server) createRedeem(amount, count int) string {
	payload := map[string]any{
		"name":         "TG兑换码",
		"quota":        s.displayAmountToRaw(amount),
		"count":        count,
		"expired_time": 0,
	}
	raw, err := s.mainAPI(http.MethodPost, "/api/redemption/", payload)
	if err != nil {
		return "生成失败: " + err.Error()
	}
	codes := getDataArrayString(raw)
	lines := []string{fmt.Sprintf("生成成功：金额 %d，数量 %d，有效期永久", amount, count), "兑换码："}
	lines = append(lines, codes...)
	return strings.Join(lines, "\n")
}

func (s *Server) displayAmountToRaw(amount int) int64 {
	cfg, err := s.getConfig()
	if err != nil || cfg.QuotaPer100 <= 0 {
		return int64(amount) * 500000
	}
	return int64(amount) * cfg.QuotaPer100 / 100
}

func (s *Server) quotaRawToDisplay(raw int64) float64 {
	cfg, err := s.getConfig()
	if err != nil || cfg.QuotaPer100 <= 0 {
		return float64(raw) / 500000.0
	}
	return float64(raw) * 100.0 / float64(cfg.QuotaPer100)
}

func tgCall(token, method string, payload map[string]any) ([]byte, error) {
	body, _ := json.Marshal(payload)
	resp, err := http.Post("https://api.telegram.org/bot"+token+"/"+method, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram status %d: %s", resp.StatusCode, string(raw))
	}
	return raw, nil
}

func tgSend(token, chatID, text string, markup map[string]any) ([]byte, error) {
	payload := map[string]any{"chat_id": chatID, "text": text}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return tgCall(token, "sendMessage", payload)
}

func isSuccess(raw []byte) bool {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	if v, ok := m["success"]; ok {
		if b, ok2 := v.(bool); ok2 {
			return b
		}
	}
	if v, ok := m["ok"]; ok {
		if b, ok2 := v.(bool); ok2 {
			return b
		}
	}
	return true
}

func compactMessage(raw []byte) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if msg, ok := m["message"].(string); ok && msg != "" {
			return msg
		}
	}
	return string(raw)
}

func getItems(raw []byte) []map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	data, _ := m["data"].(map[string]any)
	arr, _ := data["items"].([]any)
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if mm, ok := item.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func getTotal(raw []byte) int {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return 0
	}
	data, _ := m["data"].(map[string]any)
	return int(getMapNumber(data, "total"))
}

func getDataMap(raw []byte) map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return map[string]any{}
	}
	if data, ok := m["data"].(map[string]any); ok {
		return data
	}
	return map[string]any{}
}

func getDataArrayString(raw []byte) []string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	arr, _ := m["data"].([]any)
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, fmt.Sprintf("%v", item))
	}
	return out
}

func getMapString(m map[string]any, key string) string {
	if v, ok := m[key]; ok && v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func getMapNumber(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		switch t := v.(type) {
		case float64:
			return t
		case float32:
			return float64(t)
		case int:
			return float64(t)
		case int64:
			return float64(t)
		case json.Number:
			f, _ := t.Float64()
			return f
		case string:
			f, _ := strconv.ParseFloat(t, 64)
			return f
		}
	}
	return 0
}

func getInt64FromPath(raw []byte, p1, p2 string) int64 {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return 0
	}
	m1, _ := m[p1].(map[string]any)
	return int64(getMapNumber(m1, p2))
}

func splitAdminIDs(input string) []string {
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalizeAdminIDs(input string) string {
	return strings.Join(splitAdminIDs(input), ",")
}

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 6 {
		return "***"
	}
	return s[:3] + "***" + s[len(s)-3:]
}

func getenvDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
