package server

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/srunsoft/wsevent"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许所有来源，生产环境应该限制
	},
	HandshakeTimeout: 10 * time.Second,
}

// Server WebSocket 事件服务器
type Server struct {
	port       string
	secretKey  string // 用于验证插件的密钥，如果为空则不进行认证
	clients    map[*Client]bool
	broadcast  chan wsevent.EventMessage
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
	server     *http.Server
	ctx        context.Context
	cancel     context.CancelFunc
	logger     Logger
}

// Logger 日志接口
type Logger interface {
	Printf(format string, v ...interface{})
}

// DefaultLogger 默认日志实现
type DefaultLogger struct{}

func (l *DefaultLogger) Printf(format string, v ...interface{}) {
	log.Printf(format, v...)
}

// Client 客户端连接
type Client struct {
	conn       *websocket.Conn
	pluginName string          // 插件名称
	subscribed map[string]bool // 订阅的事件列表
	send       chan []byte     // 发送消息队列
	mu         sync.RWMutex    // 保护 subscribed map
}

// Config 服务器配置
type Config struct {
	Port      string // 监听端口
	SecretKey string // 认证密钥，如果为空则不进行认证
	Logger    Logger // 日志记录器，如果为空则使用默认日志
}

// NewServer 创建新的 WebSocket 事件服务器
func NewServer(config Config) *Server {
	if config.Logger == nil {
		config.Logger = &DefaultLogger{}
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Server{
		port:       config.Port,
		secretKey:  config.SecretKey,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan wsevent.EventMessage, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		ctx:        ctx,
		cancel:     cancel,
		logger:     config.Logger,
	}
}

// Start 启动服务器
func (s *Server) Start() error {
	// 启动事件广播协程
	go s.run()

	// 设置 HTTP 路由
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleWebSocket)

	s.server = &http.Server{
		Addr:    ":" + s.port,
		Handler: mux,
	}

	s.logger.Printf("WebSocket 事件服务器启动在端口: %s", s.port)

	// 启动 HTTP 服务器
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Printf("服务器启动失败: %v", err)
		}
	}()

	return nil
}

// Stop 停止服务器
func (s *Server) Stop() error {
	s.cancel()

	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(ctx); err != nil {
			s.logger.Printf("服务器关闭失败: %v", err)
			return err
		}
	}

	// 关闭所有客户端连接
	s.mu.Lock()
	for client := range s.clients {
		client.conn.Close()
		close(client.send)
	}
	s.clients = make(map[*Client]bool)
	s.mu.Unlock()

	s.logger.Printf("服务器已停止")
	return nil
}

// Emit 触发事件（供用户调用）
func (s *Server) Emit(event string, data interface{}) error {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return err
	}

	select {
	case s.broadcast <- wsevent.EventMessage{
		Event: event,
		Data:  dataBytes,
	}:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
		s.logger.Printf("广播队列已满，事件 %s 被丢弃", event)
		return nil
	}
}

// GetClientCount 获取当前连接的客户端数量
func (s *Server) GetClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// run 运行事件广播
func (s *Server) run() {
	for {
		select {
		case <-s.ctx.Done():
			return

		case client := <-s.register:
			s.mu.Lock()
			s.clients[client] = true
			s.mu.Unlock()
			s.logger.Printf("客户端已连接 (插件: %s)，当前连接数: %d", client.pluginName, len(s.clients))

		case client := <-s.unregister:
			s.mu.Lock()
			if _, ok := s.clients[client]; ok {
				delete(s.clients, client)
				close(client.send)
			}
			s.mu.Unlock()
			s.logger.Printf("客户端已断开 (插件: %s)，当前连接数: %d", client.pluginName, len(s.clients))

		case message := <-s.broadcast:
			s.mu.RLock()
			clients := make([]*Client, 0, len(s.clients))
			for client := range s.clients {
				clients = append(clients, client)
			}
			s.mu.RUnlock()

			// 构建事件消息
			eventData := map[string]interface{}{
				"event": message.Event,
				"data":  json.RawMessage(message.Data),
			}
			data, err := json.Marshal(eventData)
			if err != nil {
				s.logger.Printf("序列化事件消息失败: %v", err)
				continue
			}

			// 发送给所有订阅了该事件的客户端
			for _, client := range clients {
				client.mu.RLock()
				subscribed := client.subscribed[message.Event]
				client.mu.RUnlock()

				if subscribed {
					// 非阻塞发送到队列
					select {
					case client.send <- data:
					default:
						// 队列满了，跳过这条消息
						s.logger.Printf("客户端 %s 发送队列已满，跳过消息", client.pluginName)
					}
				}
			}
		}
	}
}

// handleWebSocket 处理 WebSocket 连接
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Printf("WebSocket 升级失败: %v", err)
		return
	}

	client := &Client{
		conn:       conn,
		subscribed: make(map[string]bool),
		send:       make(chan []byte, 256), // 每个客户端独立的发送队列，缓冲256条消息
	}

	// 启动发送协程（异步发送，不阻塞）
	go s.writePump(client)

	// 读取客户端消息
	s.readPump(client)
}

// verifyToken 验证 token
func (s *Server) verifyToken(token, pluginName string) bool {
	if s.secretKey == "" {
		return true // 如果没有设置密钥，允许所有连接
	}
	// 计算 SHA1(密钥 + 插件名称)
	h := sha1.New()
	h.Write([]byte(s.secretKey + pluginName))
	expectedToken := hex.EncodeToString(h.Sum(nil))
	return token == expectedToken
}

// readPump 读取客户端消息
func (s *Server) readPump(client *Client) {
	defer func() {
		s.unregister <- client
		client.conn.Close()
	}()

	// 如果设置了密钥，需要先进行认证
	if s.secretKey != "" {
		authenticated := false
		for !authenticated {
			_, message, err := client.conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					s.logger.Printf("WebSocket 错误: %v", err)
				}
				return
			}

			if len(message) == 0 {
				continue
			}

			// 解析认证请求
			var authReq wsevent.AuthRequest
			if err := json.Unmarshal(message, &authReq); err != nil {
				s.logger.Printf("认证请求解析失败: %v", err)
				client.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "Invalid auth request"))
				return
			}

			// 验证 token
			if !s.verifyToken(authReq.Token, authReq.PluginName) {
				s.logger.Printf("认证失败: 插件 %s token 验证失败", authReq.PluginName)
				client.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "Authentication failed"))
				return
			}

			// 认证成功
			client.pluginName = authReq.PluginName
			authenticated = true
			s.logger.Printf("插件 %s 认证成功", authReq.PluginName)

			// 发送认证成功响应
			response := map[string]interface{}{
				"status":  "authenticated",
				"message": "认证成功",
			}
			responseData, _ := json.Marshal(response)
			client.conn.WriteMessage(websocket.TextMessage, responseData)
		}
	} else {
		s.logger.Printf("未设置密钥，跳过认证流程")
	}

	// 设置读取超时
	client.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	client.conn.SetPongHandler(func(string) error {
		client.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// 处理正常的 JSON-RPC 请求
	for {
		_, message, err := client.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				s.logger.Printf("WebSocket 错误: %v", err)
			}
			break
		}

		if len(message) == 0 {
			continue
		}

		// 解析 JSON-RPC 请求
		var req wsevent.JSONRPCRequest
		if err := json.Unmarshal(message, &req); err != nil {
			s.sendError(client, nil, -32700, "Parse error", err.Error())
			continue
		}

		// 处理请求
		s.handleRequest(client, &req)
	}
}

// writePump 异步发送消息到客户端（不阻塞）
func (s *Server) writePump(client *Client) {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		client.conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.send:
			client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// 通道已关闭，发送关闭消息
				client.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// 批量发送队列中的消息
			messages := [][]byte{message}
			n := len(client.send)
			for i := 0; i < n && i < 10; i++ { // 限制批量数量
				select {
				case msg := <-client.send:
					messages = append(messages, msg)
				default:
					break
				}
			}

			// 发送所有消息
			w, err := client.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			for _, msg := range messages {
				w.Write(msg)
			}
			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			// 发送 ping 保持连接
			client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := client.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleRequest 处理 JSON-RPC 请求
func (s *Server) handleRequest(client *Client, req *wsevent.JSONRPCRequest) {
	var response wsevent.JSONRPCResponse
	response.JSONRPC = "2.0"
	response.ID = req.ID

	switch req.Method {
	case "subscribe":
		// 订阅事件
		var eventName string
		if params, ok := req.Params.(map[string]interface{}); ok {
			if e, ok := params["event"].(string); ok {
				eventName = e
			}
		}

		if eventName != "" {
			client.mu.Lock()
			client.subscribed[eventName] = true
			client.mu.Unlock()
			response.Result = map[string]interface{}{
				"status":  "subscribed",
				"event":   eventName,
				"message": "成功订阅 " + eventName + " 事件",
			}
			s.logger.Printf("客户端 %s 已订阅事件: %s", client.pluginName, eventName)
		} else {
			response.Error = &wsevent.RPCError{
				Code:    -32602,
				Message: "Invalid params: event name required",
			}
		}

	case "unsubscribe":
		// 取消订阅
		var eventName string
		if params, ok := req.Params.(map[string]interface{}); ok {
			if e, ok := params["event"].(string); ok {
				eventName = e
			}
		}

		if eventName != "" {
			client.mu.Lock()
			delete(client.subscribed, eventName)
			client.mu.Unlock()
			response.Result = map[string]interface{}{
				"status":  "unsubscribed",
				"event":   eventName,
				"message": "已取消订阅 " + eventName,
			}
			s.logger.Printf("客户端 %s 已取消订阅事件: %s", client.pluginName, eventName)
		} else {
			response.Error = &wsevent.RPCError{
				Code:    -32602,
				Message: "Invalid params: event name required",
			}
		}

	default:
		response.Error = &wsevent.RPCError{
			Code:    -32601,
			Message: "Method not found",
		}
	}

	// 发送响应
	responseData, err := json.Marshal(response)
	if err != nil {
		s.logger.Printf("序列化响应失败: %v", err)
		return
	}

	client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := client.conn.WriteMessage(websocket.TextMessage, responseData); err != nil {
		s.logger.Printf("发送响应失败: %v", err)
	}
}

// sendError 发送错误响应
func (s *Server) sendError(client *Client, id interface{}, code int, message, data string) {
	response := wsevent.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &wsevent.RPCError{
			Code:    code,
			Message: message,
		},
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		s.logger.Printf("序列化错误响应失败: %v", err)
		return
	}

	client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := client.conn.WriteMessage(websocket.TextMessage, responseData); err != nil {
		s.logger.Printf("发送错误响应失败: %v", err)
	}
}

