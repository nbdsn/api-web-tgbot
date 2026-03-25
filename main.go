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
	"net/url"
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

	MainBaseURL    string
	MainUsername   string
	MainPassword   string
	MainUseDB      bool
	MainDBPath     string
	ProxyEnabled   bool
	ProxyURL       string
	TGAPIBase      string
	PanelBaseURL   string

	BotEnabled  bool
	BotToken    string
	BotAdminIDs string
	PollSec     int

	QuotaPer100 int64
	AutoQuotaEnabled      bool
	AutoExecTime          string
	LowQuotaThreshold     int
	LowQuotaTarget        int
	HighQuotaThreshold    int
	HighQuotaTarget       int
	AutoWhitelist         string
	AutoReportToAdmin     bool
	LastAutoProcessDate   string

	CreatedAt int64
	UpdatedAt int64
}

type AutoQuotaLog struct {
	ID          uint `gorm:"primaryKey"`
	ProcessDate string
	Username    string
	Action      string
	Reason      string
	BeforeQuota float64
	AfterQuota  float64
	DeltaQuota  float64
	CreatedAt   int64
}

type Server struct {
	db *gorm.DB

	clientMu     sync.Mutex
	mainClient   *http.Client
	mainBaseURL  string
	mainUserID   int
	sessionAlive bool

	tgOffsetMu sync.Mutex
	tgOffset   int

	commandMu sync.Mutex
	cmdToken  string
	cmdAt     int64

	autoRunMu sync.Mutex
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
	if err := db.AutoMigrate(&AdminAccount{}, &AppConfig{}, &AutoQuotaLog{}); err != nil {
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
		authed.POST("/api/tg/test", s.apiTGSendTest)
		authed.POST("/api/auto-quota/run", s.apiRunAutoQuotaNow)
		authed.GET("/api/auto-quota/logs", s.apiAutoQuotaLogs)

		authed.GET("/api/channels", s.apiGetChannels)
		authed.POST("/api/channels", s.apiCreateChannel)
		authed.PUT("/api/channels/:id", s.apiUpdateChannel)
		authed.DELETE("/api/channels/:id", s.apiDeleteChannel)
	}

	go s.telegramLoop()
	go s.autoQuotaLoop()

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
			ID:               1,
			MainBaseURL:      "http://127.0.0.1:3000",
			MainUseDB:        false,
			ProxyEnabled:     false,
			TGAPIBase:        "https://api.telegram.org",
			PanelBaseURL:     "",
			BotEnabled:       false,
			PollSec:          5,
			QuotaPer100:      50000000,
			AutoExecTime:     "00:00",
			LowQuotaThreshold:  20,
			LowQuotaTarget:     100,
			HighQuotaThreshold: 200,
			HighQuotaTarget:    80,
			AutoReportToAdmin:  true,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		return db.Create(&cfg).Error
	}
	changed := false
	if strings.TrimSpace(cfg.TGAPIBase) == "" {
		cfg.TGAPIBase = "https://api.telegram.org"
		changed = true
	}
	if cfg.QuotaPer100 <= 0 {
		cfg.QuotaPer100 = 50000000
		changed = true
	}
	if cfg.PollSec <= 0 {
		cfg.PollSec = 5
		changed = true
	}
	if strings.TrimSpace(cfg.AutoExecTime) == "" {
		cfg.AutoExecTime = "00:00"
		changed = true
	}
	if cfg.LowQuotaThreshold <= 0 {
		cfg.LowQuotaThreshold = 20
		changed = true
	}
	if cfg.LowQuotaTarget <= 0 {
		cfg.LowQuotaTarget = 100
		changed = true
	}
	if cfg.HighQuotaThreshold <= 0 {
		cfg.HighQuotaThreshold = 200
		changed = true
	}
	if cfg.HighQuotaTarget <= 0 {
		cfg.HighQuotaTarget = 80
		changed = true
	}
	if changed {
		cfg.UpdatedAt = time.Now().Unix()
		return db.Save(&cfg).Error
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
	cfg.ProxyURL = maskSecret(cfg.ProxyURL)
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

	if strings.TrimSpace(req.MainBaseURL) != "" {
		cfg.MainBaseURL = strings.TrimSpace(req.MainBaseURL)
	}
	if strings.TrimSpace(req.MainUsername) != "" {
		cfg.MainUsername = strings.TrimSpace(req.MainUsername)
	}
	cfg.MainUseDB = req.MainUseDB
	cfg.MainDBPath = strings.TrimSpace(req.MainDBPath)
	if !cfg.MainUseDB {
		cfg.MainDBPath = ""
	}
	cfg.ProxyEnabled = req.ProxyEnabled
	if req.ProxyURL != "" && !strings.Contains(req.ProxyURL, "***") {
		cfg.ProxyURL = strings.TrimSpace(req.ProxyURL)
	}
	if strings.TrimSpace(req.TGAPIBase) != "" {
		cfg.TGAPIBase = strings.TrimSpace(req.TGAPIBase)
	}
	if strings.TrimSpace(req.PanelBaseURL) != "" {
		cfg.PanelBaseURL = strings.TrimSpace(req.PanelBaseURL)
	}
	if req.MainPassword != "" && !strings.Contains(req.MainPassword, "***") {
		cfg.MainPassword = req.MainPassword
	}

	cfg.BotEnabled = req.BotEnabled
	if req.BotToken != "" && !strings.Contains(req.BotToken, "***") {
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
	cfg.AutoQuotaEnabled = req.AutoQuotaEnabled
	if req.AutoExecTime != "" {
		cfg.AutoExecTime = req.AutoExecTime
	}
	if req.LowQuotaThreshold > 0 {
		cfg.LowQuotaThreshold = req.LowQuotaThreshold
	}
	if req.LowQuotaTarget > 0 {
		cfg.LowQuotaTarget = req.LowQuotaTarget
	}
	if req.HighQuotaThreshold > 0 {
		cfg.HighQuotaThreshold = req.HighQuotaThreshold
	}
	if req.HighQuotaTarget > 0 {
		cfg.HighQuotaTarget = req.HighQuotaTarget
	}
	cfg.AutoWhitelist = normalizeWhitelist(req.AutoWhitelist)
	cfg.AutoReportToAdmin = req.AutoReportToAdmin
	cfg.UpdatedAt = time.Now().Unix()

	if err := s.db.Save(&cfg).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "message": err.Error()})
		return
	}

	s.invalidateMainSession()
	c.JSON(200, gin.H{"success": true})
}

func (s *Server) apiMainHealth(c *gin.Context) {
	checks := make([]string, 0, 3)
	if _, err := s.mainAPI(http.MethodGet, "/api/status", nil); err != nil {
		c.JSON(200, gin.H{"success": false, "message": "状态检查失败: " + err.Error()})
		return
	}
	checks = append(checks, "status ok")
	if _, err := s.mainAPI(http.MethodGet, "/api/user/?p=0&page_size=1", nil); err != nil {
		c.JSON(200, gin.H{"success": false, "message": "用户接口不可用: " + err.Error()})
		return
	}
	checks = append(checks, "user api ok")
	if _, err := s.mainAPI(http.MethodGet, "/api/channel/?p=0&page_size=1&id_sort=true&tag_mode=true", nil); err != nil {
		c.JSON(200, gin.H{"success": false, "message": "渠道接口不可用: " + err.Error()})
		return
	}
	checks = append(checks, "channel api ok")
	c.JSON(200, gin.H{"success": true, "message": "连接可用，可执行实际管理操作", "checks": checks})
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

func (s *Server) invalidateMainSession() {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()
	s.mainUserID = 0
	s.sessionAlive = false
	s.mainClient = nil
}

func buildHTTPClient(proxyEnabled bool, proxyRaw string, withJar bool) (*http.Client, error) {
	transport := &http.Transport{}
	if proxyEnabled && strings.TrimSpace(proxyRaw) != "" {
		pURL, err := url.Parse(strings.TrimSpace(proxyRaw))
		if err != nil {
			return nil, fmt.Errorf("代理地址格式错误: %w", err)
		}
		transport.Proxy = http.ProxyURL(pURL)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	if withJar {
		jar, _ := cookiejar.New(nil)
		client.Jar = jar
	}
	return client, nil
}

func (s *Server) getMainClient(cfg AppConfig) (*http.Client, error) {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()

	if s.mainClient == nil || s.mainBaseURL != cfg.MainBaseURL {
		client, cErr := buildHTTPClient(cfg.ProxyEnabled, cfg.ProxyURL, true)
		if cErr != nil {
			return nil, cErr
		}
		s.mainClient = client
		s.mainBaseURL = cfg.MainBaseURL
		s.mainUserID = 0
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
		userID := getIntFromPath(raw, "data", "id")
		if userID <= 0 {
			return nil, fmt.Errorf("主程序登录失败: 未获取到用户ID")
		}
		s.mainUserID = userID
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
	if s.mainUserID > 0 {
		req.Header.Set("New-Api-User", strconv.Itoa(s.mainUserID))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		s.clientMu.Lock()
		s.mainUserID = 0
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

		s.syncCommands(cfg)
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

func (s *Server) syncCommands(cfg AppConfig) {
	s.commandMu.Lock()
	if s.cmdToken == cfg.BotToken && time.Now().Unix()-s.cmdAt < 3600 {
		s.commandMu.Unlock()
		return
	}
	s.cmdToken = cfg.BotToken
	s.cmdAt = time.Now().Unix()
	s.commandMu.Unlock()

	commands := map[string]any{
		"commands": []map[string]string{
			{"command": "start", "description": "打开管理菜单"},
			{"command": "help", "description": "查看命令帮助"},
			{"command": "stats", "description": "使用统计和性能指标"},
			{"command": "users", "description": "用户交互管理"},
			{"command": "redeem", "description": "交互生成兑换码"},
			{"command": "autoquota_now", "description": "立即执行自动额度处理"},
			{"command": "open_admin", "description": "打开管理后台地址"},
			{"command": "open_main", "description": "打开主程序地址"},
		},
	}
	_, _ = s.tgCall(cfg, "setMyCommands", commands)
	_, _ = s.tgCall(cfg, "setChatMenuButton", map[string]any{
		"menu_button": map[string]any{"type": "commands"},
	})
}

func (s *Server) pollTelegram(cfg AppConfig) error {
	s.tgOffsetMu.Lock()
	offset := s.tgOffset
	s.tgOffsetMu.Unlock()

	client, err := buildHTTPClient(cfg.ProxyEnabled, cfg.ProxyURL, false)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/getUpdates?timeout=25&offset=%d", strings.TrimRight(s.tgBase(cfg), "/"), cfg.BotToken, offset)
	resp, err := client.Get(url)
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

	text, markup := s.executeTGCommand(cfg, strings.TrimSpace(msg.Text))
	_, _ = s.tgSend(cfg, strconv.FormatInt(msg.Chat.ID, 10), text, markup)
}

func (s *Server) handleTGCallback(cfg AppConfig, cb *TelegramCallback) {
	if cb == nil || cb.From == nil {
		return
	}
	if !s.isTGAdmin(cfg, cb.From.ID) {
		_, _ = s.tgCall(cfg, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": "无权限"})
		return
	}

	text, markup, ack := s.executeTGCallback(cb.Data)
	if text != "" {
		sent := false
		if cb.Message != nil && cb.Message.MessageID > 0 && cb.Message.Chat.ID != 0 {
			if _, err := s.tgEdit(cfg, strconv.FormatInt(cb.Message.Chat.ID, 10), cb.Message.MessageID, text, markup); err == nil {
				sent = true
			} else {
				log.Printf("telegram: edit callback message failed: %v", err)
			}
		}
		if !sent {
			targetChatID := strconv.FormatInt(cb.From.ID, 10)
			if cb.Message != nil && cb.Message.Chat.ID != 0 {
				targetChatID = strconv.FormatInt(cb.Message.Chat.ID, 10)
			}
			if _, err := s.tgSend(cfg, targetChatID, text, markup); err != nil {
				log.Printf("telegram: send callback response failed: %v", err)
				if ack == "" || ack == "已处理" {
					ack = "处理失败，请看服务日志"
				}
			}
		}
	}
	if ack == "" {
		ack = "已处理"
	}
	_, _ = s.tgCall(cfg, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": ack})
}

func (s *Server) executeTGCommand(cfg AppConfig, text string) (string, map[string]any) {
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
	case "/autoquota_now":
		msg, err := s.runAutoQuotaProcess(true)
		if err != nil {
			return "立即执行失败: " + err.Error(), nil
		}
		return msg, nil
	case "/open_admin":
		if strings.TrimSpace(cfg.PanelBaseURL) == "" {
			return "未配置管理后台地址，请到 TG 功能页面填写“管理后台地址（用于 /open_admin）”。", nil
		}
		return "管理后台地址:\n" + strings.TrimSpace(cfg.PanelBaseURL), nil
	case "/open_main":
		if strings.TrimSpace(cfg.MainBaseURL) == "" {
			return "未配置主程序地址，请到主程序连接页面填写。", nil
		}
		return "主程序地址:\n" + strings.TrimSpace(cfg.MainBaseURL), nil
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
		"/autoquota_now 立即执行自动额度处理并返回日志",
		"/open_admin 返回管理后台地址",
		"/open_main 返回主程序地址",
	}, "\n")
}

func (s *Server) tgStats() string {
	statusRaw, err := s.mainAPI(http.MethodGet, "/api/status", nil)
	if err != nil {
		return "统计失败: " + userFacingMainErr(err)
	}
	quotaPerUnit := getInt64FromPath(statusRaw, "data", "quota_per_unit")
	if quotaPerUnit <= 0 {
		quotaPerUnit = 500000
	}

	now := time.Now().Unix()
	since := now - 86400

	logStatRaw, err := s.mainAPI(http.MethodGet, fmt.Sprintf("/api/log/stat?type=2&start_timestamp=%d&end_timestamp=%d", since, now), nil)
	if err != nil {
		return "统计失败: " + userFacingMainErr(err)
	}

	rpmRealtime := getInt64FromPath(logStatRaw, "data", "rpm")
	tpmRealtime := getInt64FromPath(logStatRaw, "data", "tpm")
	quota24Raw := getInt64FromPath(logStatRaw, "data", "quota")

	usersRaw, err := s.mainAPI(http.MethodGet, "/api/user/?p=0&page_size=1000", nil)
	if err != nil {
		return "统计失败: " + userFacingMainErr(err)
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
		return "统计失败: " + userFacingMainErr(err)
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
		return "读取用户失败: " + userFacingMainErr(err), nil
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
		return "读取用户失败: " + userFacingMainErr(err), nil, "读取失败"
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
		return "状态更新失败: " + userFacingMainErr(err)
	}
	if enable {
		return "已启用账户"
	}
	return "已停用账户"
}

func (s *Server) applyUserQuota(uid int, increase bool, amount int) string {
	raw, err := s.mainAPI(http.MethodGet, fmt.Sprintf("/api/user/%d", uid), nil)
	if err != nil {
		return "读取用户失败: " + userFacingMainErr(err)
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
		return "额度更新失败: " + userFacingMainErr(err)
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
		return "生成失败: " + userFacingMainErr(err)
	}
	codes := getDataArrayString(raw)
	lines := []string{fmt.Sprintf("生成成功：金额 %d，数量 %d，有效期永久", amount, count), "兑换码："}
	lines = append(lines, codes...)
	return strings.Join(lines, "\n")
}

func (s *Server) autoQuotaLoop() {
	for {
		cfg, err := s.getConfig()
		if err != nil {
			time.Sleep(20 * time.Second)
			continue
		}
		if !cfg.AutoQuotaEnabled {
			time.Sleep(20 * time.Second)
			continue
		}
		hour, minute := parseHM(cfg.AutoExecTime)
		now := time.Now()
		today := now.Format("2006-01-02")
		if cfg.LastAutoProcessDate == today {
			time.Sleep(20 * time.Second)
			continue
		}
		if now.Hour() == hour && now.Minute() >= minute {
			_, _ = s.runAutoQuotaProcess(false)
		}
		time.Sleep(20 * time.Second)
	}
}

func (s *Server) apiRunAutoQuotaNow(c *gin.Context) {
	msg, err := s.runAutoQuotaProcess(true)
	if err != nil {
		c.JSON(200, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"success": true, "message": msg})
}

func (s *Server) apiAutoQuotaLogs(c *gin.Context) {
	limit := 200
	if p := strings.TrimSpace(c.Query("limit")); p != "" {
		if i, err := strconv.Atoi(p); err == nil && i > 0 && i <= 1000 {
			limit = i
		}
	}
	items := make([]AutoQuotaLog, 0)
	if err := s.db.Order("id desc").Limit(limit).Find(&items).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": items})
}

func (s *Server) runAutoQuotaProcess(force bool) (string, error) {
	s.autoRunMu.Lock()
	defer s.autoRunMu.Unlock()

	cfg, err := s.getConfig()
	if err != nil {
		return "", err
	}
	if !force && !cfg.AutoQuotaEnabled {
		return "未启用自动额度处理", nil
	}
	if cfg.LowQuotaTarget <= 0 || cfg.HighQuotaTarget <= 0 || cfg.LowQuotaThreshold <= 0 || cfg.HighQuotaThreshold <= 0 {
		return "", fmt.Errorf("自动额度参数不完整")
	}

	raw, err := s.mainAPI(http.MethodGet, "/api/user/?p=0&page_size=1000", nil)
	if err != nil {
		return "", err
	}
	users := getItems(raw)
	whiteSet := whitelistSet(cfg.AutoWhitelist)
	changes := make([]AutoQuotaLog, 0)

	for _, u := range users {
		username := strings.TrimSpace(getMapString(u, "username"))
		if username == "" {
			continue
		}
		if _, ok := whiteSet[username]; ok {
			continue
		}
		quotaRaw := int64(getMapNumber(u, "quota"))
		before := s.quotaRawToDisplay(quotaRaw)
		after := before
		action := ""
		reason := ""
		if before < float64(cfg.LowQuotaThreshold) {
			after = float64(cfg.LowQuotaTarget)
			action = "增加"
			reason = fmt.Sprintf("低于阈值 %d", cfg.LowQuotaThreshold)
		} else if before > float64(cfg.HighQuotaThreshold) {
			after = float64(cfg.HighQuotaTarget)
			action = "减少"
			reason = fmt.Sprintf("高于阈值 %d", cfg.HighQuotaThreshold)
		}
		if action == "" {
			continue
		}
		newRaw := s.displayValueToRaw(after)
		if newRaw < 0 {
			newRaw = 0
		}
		payload := map[string]any{
			"id":           int(getMapNumber(u, "id")),
			"username":     username,
			"display_name": getMapString(u, "display_name"),
			"role":         int(getMapNumber(u, "role")),
			"group":        getMapString(u, "group"),
			"quota":        newRaw,
			"remark":       getMapString(u, "remark"),
		}
		if _, err := s.mainAPI(http.MethodPut, "/api/user/", payload); err != nil {
			log.Printf("auto quota update failed user=%s err=%v", username, err)
			continue
		}
		delta := after - before
		entry := AutoQuotaLog{
			ProcessDate: time.Now().Format("2006-01-02"),
			Username:    username,
			Action:      action,
			Reason:      reason,
			BeforeQuota: before,
			AfterQuota:  after,
			DeltaQuota:  delta,
			CreatedAt:   time.Now().Unix(),
		}
		changes = append(changes, entry)
	}

	if len(changes) > 0 {
		for _, item := range changes {
			_ = s.db.Create(&item).Error
		}
	}
	cfg.LastAutoProcessDate = time.Now().Format("2006-01-02")
	cfg.UpdatedAt = time.Now().Unix()
	_ = s.db.Save(&cfg).Error

	report := buildAutoQuotaReport(changes, cfg)
	if cfg.AutoReportToAdmin && cfg.BotEnabled && strings.TrimSpace(cfg.BotToken) != "" {
		adminIDs := splitAdminIDs(cfg.BotAdminIDs)
		for _, id := range adminIDs {
			_, _ = s.tgSend(cfg, id, report, nil)
		}
	}
	return report, nil
}

func buildAutoQuotaReport(changes []AutoQuotaLog, cfg AppConfig) string {
	lines := []string{
		fmt.Sprintf("每日额度处理报告 %s", time.Now().Format("2006-01-02 15:04:05")),
		fmt.Sprintf("规则: < %d -> %d, > %d -> %d", cfg.LowQuotaThreshold, cfg.LowQuotaTarget, cfg.HighQuotaThreshold, cfg.HighQuotaTarget),
	}
	if len(changes) == 0 {
		lines = append(lines, "本次没有用户被调整。")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, fmt.Sprintf("处理总数: %d", len(changes)))
	for _, c := range changes {
		lines = append(lines, fmt.Sprintf("用户 %s | %s | 处理前 %.2f | 处理后 %.2f | 变动 %.2f | %s", c.Username, c.Action, c.BeforeQuota, c.AfterQuota, c.DeltaQuota, c.Reason))
	}
	return strings.Join(lines, "\n")
}

func parseHM(hm string) (int, int) {
	hm = strings.TrimSpace(hm)
	if hm == "" {
		return 0, 0
	}
	parts := strings.Split(hm, ":")
	if len(parts) != 2 {
		return 0, 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	if h < 0 || h > 23 {
		h = 0
	}
	if m < 0 || m > 59 {
		m = 0
	}
	return h, m
}

func (s *Server) displayAmountToRaw(amount int) int64 {
	cfg, err := s.getConfig()
	if err != nil || cfg.QuotaPer100 <= 0 {
		return int64(amount) * 500000
	}
	return int64(amount) * cfg.QuotaPer100 / 100
}

func (s *Server) displayValueToRaw(amount float64) int64 {
	cfg, err := s.getConfig()
	if err != nil || cfg.QuotaPer100 <= 0 {
		return int64(amount * 500000.0)
	}
	return int64(amount * float64(cfg.QuotaPer100) / 100.0)
}

func (s *Server) quotaRawToDisplay(raw int64) float64 {
	cfg, err := s.getConfig()
	if err != nil || cfg.QuotaPer100 <= 0 {
		return float64(raw) / 500000.0
	}
	return float64(raw) * 100.0 / float64(cfg.QuotaPer100)
}

func (s *Server) tgBase(cfg AppConfig) string {
	if strings.TrimSpace(cfg.TGAPIBase) == "" {
		return "https://api.telegram.org"
	}
	return strings.TrimSpace(cfg.TGAPIBase)
}

func (s *Server) tgCall(cfg AppConfig, method string, payload map[string]any) ([]byte, error) {
	body, _ := json.Marshal(payload)
	client, err := buildHTTPClient(cfg.ProxyEnabled, cfg.ProxyURL, false)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(s.tgBase(cfg), "/") + "/bot" + cfg.BotToken + "/" + method
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
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

func (s *Server) tgSend(cfg AppConfig, chatID, text string, markup map[string]any) ([]byte, error) {
	payload := map[string]any{"chat_id": chatID, "text": text}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return s.tgCall(cfg, "sendMessage", payload)
}

func (s *Server) tgEdit(cfg AppConfig, chatID string, messageID int, text string, markup map[string]any) ([]byte, error) {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	}
	if markup != nil {
		payload["reply_markup"] = markup
	}
	return s.tgCall(cfg, "editMessageText", payload)
}

func (s *Server) apiTGSendTest(c *gin.Context) {
	cfg, err := s.getConfig()
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": err.Error()})
		return
	}
	if strings.TrimSpace(cfg.BotToken) == "" {
		c.JSON(200, gin.H{"success": false, "message": "请先配置 TG Bot Token"})
		return
	}
	adminIDs := splitAdminIDs(cfg.BotAdminIDs)
	if len(adminIDs) == 0 {
		c.JSON(200, gin.H{"success": false, "message": "请先配置管理员 TG ID"})
		return
	}
	okCnt := 0
	failLines := make([]string, 0)
	msg := fmt.Sprintf("API-WEB-TGBOT 测试消息\n时间: %s\n状态: 发送测试成功", time.Now().Format("2006-01-02 15:04:05"))
	for _, id := range adminIDs {
		if _, err := s.tgSend(cfg, id, msg, nil); err != nil {
			failLines = append(failLines, fmt.Sprintf("%s: %v", id, err))
			continue
		}
		okCnt++
	}
	if okCnt == 0 {
		c.JSON(200, gin.H{"success": false, "message": "测试消息全部发送失败", "fails": failLines})
		return
	}
	c.JSON(200, gin.H{"success": true, "message": fmt.Sprintf("测试消息发送完成，成功 %d，失败 %d", okCnt, len(failLines)), "fails": failLines})
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

func userFacingMainErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "请先配置主程序连接") {
		return "未配置可用 API 站点，请在 Web 的主程序连接里填写地址和管理员账号密码。"
	}
	if strings.Contains(msg, "主程序登录失败") {
		return "API 站点登录失败，请检查主程序地址、管理员账号密码是否正确，或该账号是否被封禁。"
	}
	if strings.Contains(msg, "connect: connection refused") || strings.Contains(msg, "no such host") || strings.Contains(msg, "i/o timeout") {
		return "API 站点不可达，请检查地址、端口、网络或代理设置。"
	}
	return msg
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

func getIntFromPath(raw []byte, p1, p2 string) int {
	return int(getInt64FromPath(raw, p1, p2))
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

func splitWhitelist(input string) []string {
	f := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(f))
	for _, it := range f {
		it = strings.TrimSpace(it)
		if it != "" {
			out = append(out, it)
		}
	}
	return out
}

func normalizeWhitelist(input string) string {
	return strings.Join(splitWhitelist(input), ",")
}

func whitelistSet(input string) map[string]struct{} {
	items := splitWhitelist(input)
	m := make(map[string]struct{}, len(items))
	for _, it := range items {
		m[it] = struct{}{}
	}
	return m
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
