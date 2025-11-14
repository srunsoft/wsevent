package client

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/srunsoft/wsevent"
)

// Client WebSocket 事件客户端
type Client struct {
	host          string
	port          string
	conn          *websocket.Conn
	eventHandlers map[string][]EventHandler
	running       bool
	mu            sync.RWMutex
	pluginName    string // 插件名称
	secretKey     string // 密钥
	requestID     int64  // 请求 ID 计数器
	requestMu     sync.Mutex
	logger        Logger
	done          chan struct{} // 用于通知监听结束
}

// Logger 日志接口
type Logger interface {
	Printf(format string, v ...interface{})
}

// DefaultLogger 默认日志实现
type DefaultLogger struct{}

func (l *DefaultLogger) Printf(format string, v ...interface{}) {
	// 默认不输出日志
}

// EventHandler 事件处理函数类型
type EventHandler func(data interface{})

// Config 客户端配置
type Config struct {
	Address    string // 服务器地址，格式: "127.0.0.1:8085" 或 "ws://localhost:8085"
	PluginName string // 插件名称（用于认证）
	SecretKey  string // 认证密钥（如果服务器需要认证）
	Logger     Logger // 日志记录器，如果为空则不输出日志
}

// NewClient 创建新的 WebSocket 事件客户端
func NewClient(config Config) (*Client, error) {
	if config.PluginName == "" {
		return nil, fmt.Errorf("插件名称不能为空")
	}

	// 解析地址
	host, port, err := parseAddress(config.Address)
	if err != nil {
		return nil, err
	}

	if config.Logger == nil {
		config.Logger = &DefaultLogger{}
	}

	return &Client{
		host:          host,
		port:          port,
		pluginName:    config.PluginName,
		secretKey:     config.SecretKey,
		eventHandlers: make(map[string][]EventHandler),
		logger:        config.Logger,
		done:          make(chan struct{}),
	}, nil
}

// parseAddress 解析地址
func parseAddress(address string) (host, port string, err error) {
	// 支持 ws:// 或 wss:// 前缀，也支持直接 host:port
	u, err := url.Parse(address)
	if err == nil && (u.Scheme == "ws" || u.Scheme == "wss") {
		host = u.Hostname()
		if u.Port() != "" {
			port = u.Port()
		} else if u.Scheme == "ws" {
			port = "80"
		} else {
			port = "443"
		}
		return host, port, nil
	}

	// 兼容旧格式 host:port
	host, port, err = net.SplitHostPort(address)
	if err != nil {
		return "", "", fmt.Errorf("地址格式错误，应为 'host:port' 或 'ws://host:port': %v", err)
	}
	return host, port, nil
}

// generateToken 生成认证 token
func (c *Client) generateToken() string {
	if c.secretKey == "" {
		return ""
	}
	h := sha1.New()
	h.Write([]byte(c.secretKey + c.pluginName))
	return hex.EncodeToString(h.Sum(nil))
}

// nextRequestID 获取下一个请求 ID
func (c *Client) nextRequestID() int64 {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	c.requestID++
	return c.requestID
}

// Connect 连接到服务器
func (c *Client) Connect() error {
	if c.conn != nil {
		return nil
	}

	// 构建 WebSocket URL
	u := url.URL{
		Scheme: "ws",
		Host:   fmt.Sprintf("%s:%s", c.host, c.port),
		Path:   "/",
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("WebSocket 连接失败: %v", err)
	}

	c.conn = conn

	// 如果设置了密钥，进行认证
	if c.secretKey != "" {
		token := c.generateToken()
		authReq := map[string]interface{}{
			"token":       token,
			"plugin_name": c.pluginName,
		}
		authData, err := json.Marshal(authReq)
		if err != nil {
			conn.Close()
			return fmt.Errorf("序列化认证请求失败: %v", err)
		}

		if err := conn.WriteMessage(websocket.TextMessage, authData); err != nil {
			conn.Close()
			return fmt.Errorf("发送认证请求失败: %v", err)
		}

		// 读取认证响应
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, response, err := conn.ReadMessage()
		if err != nil {
			conn.Close()
			return fmt.Errorf("读取认证响应失败: %v", err)
		}

		var authResp map[string]interface{}
		if err := json.Unmarshal(response, &authResp); err != nil {
			conn.Close()
			return fmt.Errorf("解析认证响应失败: %v", err)
		}

		if status, ok := authResp["status"].(string); !ok || status != "authenticated" {
			conn.Close()
			return fmt.Errorf("认证失败: %v", authResp)
		}

		c.logger.Printf("认证成功")
	}

	// 启动读取协程
	go c.readPump()

	return nil
}

// On 订阅事件
func (c *Client) On(eventName string, handler EventHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.eventHandlers[eventName] == nil {
		c.eventHandlers[eventName] = make([]EventHandler, 0)
	}
	c.eventHandlers[eventName] = append(c.eventHandlers[eventName], handler)

	// 如果已经连接，立即订阅
	if c.conn != nil {
		go c.subscribe(eventName)
	}
}

// subscribe 订阅服务器事件
func (c *Client) subscribe(eventName string) error {
	request := wsevent.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      c.nextRequestID(),
		Method:  "subscribe",
		Params: map[string]interface{}{
			"event": eventName,
		},
	}

	data, err := json.Marshal(request)
	if err != nil {
		return err
	}

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("未连接到服务器")
	}

	// 发送请求
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return err
	}

	// 读取响应（简化处理，不等待响应）
	// 实际响应会在 readPump 中处理
	return nil
}

// Off 取消订阅事件
func (c *Client) Off(eventName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.eventHandlers, eventName)

	if c.conn != nil {
		request := wsevent.JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      c.nextRequestID(),
			Method:  "unsubscribe",
			Params: map[string]interface{}{
				"event": eventName,
			},
		}

		data, _ := json.Marshal(request)
		c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		c.conn.WriteMessage(websocket.TextMessage, data)
	}
}

// Listen 开始监听事件（阻塞）
func (c *Client) Listen() error {
	// 连接服务器
	if err := c.Connect(); err != nil {
		return err
	}

	// 订阅所有已注册的事件
	c.mu.RLock()
	eventNames := make([]string, 0, len(c.eventHandlers))
	for eventName := range c.eventHandlers {
		eventNames = append(eventNames, eventName)
	}
	c.mu.RUnlock()

	for _, eventName := range eventNames {
		if err := c.subscribe(eventName); err != nil {
			c.logger.Printf("订阅事件 %s 失败: %v", eventName, err)
		}
	}

	c.mu.Lock()
	c.running = true
	c.mu.Unlock()

	// 等待运行结束
	<-c.done
	return nil
}

// readPump 读取消息
func (c *Client) readPump() {
	defer func() {
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
		c.running = false
		c.mu.Unlock()
		close(c.done)
	}()

	// 设置读取超时和 ping 处理
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// 启动 ping 协程
	pingTicker := time.NewTicker(54 * time.Second)
	defer pingTicker.Stop()

	go func() {
		for {
			select {
			case <-pingTicker.C:
				c.mu.RLock()
				conn := c.conn
				c.mu.RUnlock()
				if conn == nil {
					return
				}
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-c.done:
				return
			}
		}
	}()

	// 监听事件
	for {
		c.mu.RLock()
		running := c.running
		conn := c.conn
		c.mu.RUnlock()

		if !running || conn == nil {
			break
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.logger.Printf("WebSocket 连接已断开: %v", err)
			}
			break
		}

		if len(message) == 0 {
			continue
		}

		// 尝试解析为事件消息
		var eventMsg map[string]interface{}
		if err := json.Unmarshal(message, &eventMsg); err != nil {
			// 可能是 JSON-RPC 响应，忽略
			continue
		}

		// 检查是否是事件消息
		if event, ok := eventMsg["event"].(string); ok {
			if data, ok := eventMsg["data"]; ok {
				c.mu.RLock()
				handlers := c.eventHandlers[event]
				c.mu.RUnlock()

				// 调用所有注册的处理函数
				for _, handler := range handlers {
					go handler(data)
				}
			}
		}
	}
}

// Stop 停止监听
func (c *Client) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.running = false
	if c.conn != nil {
		c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		c.conn.Close()
		c.conn = nil
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

// IsConnected 检查是否已连接
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn != nil
}

