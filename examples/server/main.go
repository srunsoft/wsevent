package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/srunsoft/wsevent/server"
)

func main() {
	// 从环境变量读取密钥（可选）
	secretKey := os.Getenv("WSEVENT_SECRET_KEY")
	if secretKey == "" {
		secretKey = "my-secret-key" // 默认密钥，生产环境应该从环境变量读取
	}

	// 创建服务器
	srv := server.NewServer(server.Config{
		Port:      "8085",
		SecretKey: secretKey,
		Logger:    &server.DefaultLogger{}, // 使用默认日志
	})

	// 启动服务器
	if err := srv.Start(); err != nil {
		log.Fatalf("启动服务器失败: %v", err)
	}

	// 模拟事件触发
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		eventCount := 0
		for range ticker.C {
			eventCount++
			// 触发事件
			err := srv.Emit("user_login", map[string]interface{}{
				"user_id": 12345,
				"username": "john_doe",
				"timestamp": time.Now().Unix(),
			})
			if err != nil {
				log.Printf("触发事件失败: %v", err)
			} else {
				log.Printf("已触发事件: user_login (第 %d 次)", eventCount)
			}

			// 触发另一个事件
			srv.Emit("system_notification", map[string]interface{}{
				"type":    "info",
				"message": "系统运行正常",
				"count":   eventCount,
			})
		}
	}()

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("正在关闭服务器...")
	if err := srv.Stop(); err != nil {
		log.Printf("关闭服务器失败: %v", err)
	}
	log.Println("服务器已关闭")
}

