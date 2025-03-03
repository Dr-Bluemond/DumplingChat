package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
	"github.com/sirupsen/logrus"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	WhitelistFile   = "/mine/chat/whitelist.txt"
	DatabasePath    = "/mine/chat/db.sqlite"
	UploadDirectory = "/mine/chat/files/"
	StaticDirectory = "/mine/chat/static/"
)

type ConnInfo struct {
	Nickname  string
	SendChan  chan Message
	CloseChan chan struct{}
}

var (
	ipNicknameMap   map[string]string
	ipMapMutex      sync.RWMutex
	upgrader        = websocket.Upgrader{}
	wsConnections   = make(map[*websocket.Conn]*ConnInfo)
	wsConnectionsMu sync.RWMutex
	db              *gorm.DB
	logger          = logrus.New()
	messageSequenceLock sync.Mutex
)

type Message struct {
	ID        uint `gorm:"primaryKey"`
	Nickname  string
	Content   string
	Type      string
	FileID    uint
	Timestamp time.Time
}

type File struct {
	ID           uint `gorm:"primaryKey"`
	OriginalName string
	StoredName   string `gorm:"unique"`
}

func init() {
	logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	loadWhitelist()
	initDatabase()
	createUploadDirectory()
}

func main() {
	r := gin.Default()

	if err := r.SetTrustedProxies([]string{}); err != nil {
		logger.Info("Failed to SetTrustedProxies")
	}
	r.Use(IPWhitelistMiddleware())

	r.GET("/ws", handleWebSocket)
	r.GET("/api/messages", getMessages)
	r.POST("/api/upload", handleUpload)
	r.GET("/api/files/:file_id", downloadFile)
	r.GET("/api/myname", getMyName)

	r.NoRoute(func(c *gin.Context) {
		c.File(filepath.Join(StaticDirectory, "index.html"))
	})
	r.Static("/static", StaticDirectory)

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			logger.Info("Reloading whitelist...")
			loadWhitelist()
		}
	}()

	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		for range ticker.C {
			logger.Info("Starting hourly cleanup...")
			cleanupOldMessagesAndFiles()
		}
	}()

	logger.Info("Server is starting on :8008")
	r.Run(":8008")
}

func IPWhitelistMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := c.ClientIP()
		ipMapMutex.RLock()
		nickname, ok := ipNicknameMap[clientIP]
		ipMapMutex.RUnlock()

		if !ok {
			logger.Warnf("IP %s not in whitelist", clientIP)
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.Set("nickname", nickname)
		c.Set("ip", clientIP)
		c.Next()
	}
}

func getMyName(c *gin.Context) {
	nickname, _ := c.Get("nickname")
	ip, _ := c.Get("ip")
	c.JSON(http.StatusOK, gin.H{"nickname": nickname, "ip": ip})
}

func handleWebSocket(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Errorf("WebSocket upgrade failed: %v", err)
		return
	}

	nickname := c.MustGet("nickname").(string)
	sendChan := make(chan Message, 100)
	closeChan := make(chan struct{}, 2)

	wsConnectionsMu.Lock()
	wsConnections[conn] = &ConnInfo{
		Nickname:  nickname,
		SendChan:  sendChan,
		CloseChan: closeChan,
	}
	wsConnectionsMu.Unlock()

	logger.Infof("WebSocket connected: %s", nickname)

	// 启动消息发送协程
	go func() {
		for {
			select {
			case msg := <-sendChan:
				if err := conn.WriteJSON(msg); err != nil {
					logger.Infof("Send failed to %s: %v", nickname, err)
					conn.Close()
					return
				}
			case <-closeChan:
				return
			}
		}
	}()

	for {
		var msg struct{ Content string }
		if err := conn.ReadJSON(&msg); err != nil {
			logger.Infof("Read error: %v", err)
			break
		}

		messageSequenceLock.Lock()
		message := Message{
			Nickname:  nickname,
			Content:   msg.Content,
			Type:      "text",
			Timestamp: time.Now(),
		}

		if err := db.Create(&message).Error; err != nil {
			logger.Errorf("Save message failed: %v", err)
		} else {
			broadcastMessage(message)
			logger.Infof("Message saved: %s", msg.Content)
		}
		messageSequenceLock.Unlock()
	}

	// 清理逻辑
	closeChan <- struct{}{}
	close(closeChan)
	wsConnectionsMu.Lock()
	delete(wsConnections, conn)
	wsConnectionsMu.Unlock()
	conn.Close()
	logger.Infof("WebSocket closed: %s", nickname)
}

func getMessages(c *gin.Context) {
	var messages []Message
	lastID := c.Query("last_id")
	query := db.Order("id desc").Limit(20)

	if lastID != "" {
		query = query.Where("id < ?", lastID)
	}

	if err := query.Find(&messages).Error; err != nil {
		logger.Errorf("Get messages failed: %v", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, messages)
}

func handleUpload(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		logger.Errorf("Get file failed: %v", err)
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := filepath.Ext(header.Filename)
	storedName := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
	filePath := filepath.Join(UploadDirectory, storedName)

	out, err := os.Create(filePath)
	if err != nil {
		logger.Errorf("Create file failed: %v", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		logger.Errorf("Copy file failed: %v", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	messageSequenceLock.Lock()
	defer messageSequenceLock.Unlock()
	fileRecord := File{OriginalName: header.Filename, StoredName: storedName}
	message := Message{
		Nickname:  c.MustGet("nickname").(string),
		Content:   header.Filename,
		Type:      "file",
		Timestamp: time.Now(),
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&fileRecord).Error; err != nil {
			return err
		}
		message.FileID = fileRecord.ID
		return tx.Create(&message).Error
	}); err != nil {
		logger.Errorf("Save file record failed: %v", err)
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	broadcastMessage(message)
	c.Status(http.StatusCreated)
}

func downloadFile(c *gin.Context) {
	fileID := c.Param("file_id")
	var fileRecord File
	if err := db.Where("id = ?", fileID).First(&fileRecord).Error; err != nil {
		logger.Errorf("File not found: %s", fileID)
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	filePath := filepath.Join(UploadDirectory, fileRecord.StoredName)
	c.FileAttachment(filePath, fileRecord.OriginalName)
}

func loadWhitelist() {
	file, err := os.Open(WhitelistFile)
	if err != nil {
		logger.Fatalf("Open whitelist failed: %v", err)
	}
	defer file.Close()

	newMap := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if dashIndex := strings.Index(line, "-"); dashIndex != -1 {
			ip := line[:dashIndex]
			nickname := line[dashIndex+1:]
			newMap[ip] = nickname
		}
	}

	ipMapMutex.Lock()
	ipNicknameMap = newMap
	ipMapMutex.Unlock()
}

func initDatabase() {
	var err error
	if db, err = gorm.Open(sqlite.Open(DatabasePath), &gorm.Config{}); err != nil {
		logger.Fatalf("DB connection failed: %v", err)
	}
	db.AutoMigrate(&Message{}, &File{})
}

func createUploadDirectory() {
	if err := os.MkdirAll(UploadDirectory, 0755); err != nil {
		logger.Fatalf("Create upload dir failed: %v", err)
	}
}

func cleanupOldMessagesAndFiles() {
	threshold := time.Now().Add(-7 * 24 * time.Hour)
	var oldMessages []Message
	var filesToDelete []File

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("timestamp < ?", threshold).Find(&oldMessages).Error; err != nil {
			return err
		}

		var fileIDs []uint
		for _, msg := range oldMessages {
			if msg.Type == "file" {
				fileIDs = append(fileIDs, msg.FileID)
			}
		}

		if len(fileIDs) > 0 {
			if err := tx.Where("id IN ?", fileIDs).Find(&filesToDelete).Error; err != nil {
				return err
			}
		}

		tx.Where("timestamp < ?", threshold).Delete(&Message{})
		if len(fileIDs) > 0 {
			tx.Where("id IN ?", fileIDs).Delete(&File{})
		}
		return nil
	})

	if err != nil {
		logger.Errorf("Cleanup failed: %v", err)
		return
	}

	for _, file := range filesToDelete {
		filePath := filepath.Join(UploadDirectory, file.StoredName)
		if err := os.Remove(filePath); err != nil {
			logger.Warnf("Failed to delete file %s: %v", filePath, err)
		}
	}

	logger.Infof("Cleanup completed. Messages: %d, Files: %d", len(oldMessages), len(filesToDelete))
}

func broadcastMessage(msg Message) {
	wsConnectionsMu.RLock()
	defer wsConnectionsMu.RUnlock()

	for conn, info := range wsConnections {
		select {
		case info.SendChan <- msg: // 非阻塞发送
		default:
			// 通道满时处理
			logger.Warnf("Connection %s buffer full, closing", info.Nickname)
			info.CloseChan <- struct{}{}
			conn.Close()
			wsConnectionsMu.Lock()
			delete(wsConnections, conn)
			wsConnectionsMu.Unlock()
		}
	}
}
