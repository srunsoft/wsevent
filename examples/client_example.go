package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/srunsoft/wsevent/client"
)

func main() {
	// 从环境变量读取密钥
	secretKey := os.Getenv("WSEVENT_SECRET_KEY")
	if secretKey == "" {
		secretKey = "my-secret-key" // 默认密钥，应该与服务器一致
	}

	// 创建客户端
	cli, err := client.NewClient(client.Config{
		Address:    "ws://127.0.0.1:8085",
		PluginName: "my-plugin",
		SecretKey:  secretKey,
		Logger:     &client.DefaultLogger{}, // 使用默认日志（不输出）
	})
	if err != nil {
		log.Fatalf("创建客户端失败: %v", err)
	}

	// 订阅事件
	cli.On("user_login", func(data interface{}) {
		// 将 data 转换为 JSON 以便查看
		dataBytes, _ := json.MarshalIndent(data, "", "  ")
		fmt.Printf("收到事件: user_login\n数据: %s\n\n", string(dataBytes))
	})

	cli.On("system_notification", func(data interface{}) {
		dataBytes, _ := json.MarshalIndent(data, "", "  ")
		fmt.Printf("收到事件: system_notification\n数据: %s\n\n", string(dataBytes))
	})

	// 启动监听（在协程中运行，以便可以处理信号）
	go func() {
		if err := cli.Listen(); err != nil {
			log.Printf("监听失败: %v", err)
		}
	}()

	// 等待连接建立
	for !cli.IsConnected() {
		// 等待连接
	}

	fmt.Println("客户端已连接，等待事件...")

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\n正在关闭客户端...")
	cli.Stop()
	fmt.Println("客户端已关闭")
}
