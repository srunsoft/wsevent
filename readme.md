# wsevent

一个基于 WebSocket 的事件驱动通信库，支持 Go 语言服务端和客户端，同时兼容其他语言通过 WebSocket 协议连接。

## 特性

- 🚀 **简单易用**: 提供简洁的 API，快速上手
- 🔐 **安全认证**: 支持基于 SHA1 的 token 认证机制
- 📡 **事件驱动**: 基于发布-订阅模式的事件系统
- 🌐 **跨语言**: 使用标准 WebSocket 和 JSON-RPC 协议，支持任何语言实现客户端
- ⚡ **高性能**: 异步非阻塞设计，支持高并发
- 🔄 **自动重连**: 客户端支持连接管理和心跳保持

## 安装

```bash
go get github.com/srunsoft/wsevent
```

## 快速开始

### 服务端

```go
package main

import (
    "log"
    "github.com/srunsoft/wsevent/server"
)

func main() {
    // 创建服务器
    srv := server.NewServer(server.Config{
        Port:      "8085",
        SecretKey: "my-secret-key", // 可选，如果为空则不进行认证
    })

    // 启动服务器
    if err := srv.Start(); err != nil {
        log.Fatalf("启动服务器失败: %v", err)
    }

    // 触发事件
    srv.Emit("user_login", map[string]interface{}{
        "user_id": 12345,
        "username": "john_doe",
    })
}
```

### 客户端

```go
package main

import (
    "fmt"
    "github.com/srunsoft/wsevent/client"
)

func main() {
    // 创建客户端
    cli, err := client.NewClient(client.Config{
        Address:    "ws://127.0.0.1:8085",
        PluginName: "my-plugin",
        SecretKey:  "my-secret-key", // 必须与服务器一致
    })
    if err != nil {
        panic(err)
    }

    // 订阅事件
    cli.On("user_login", func(data interface{}) {
        fmt.Printf("收到事件: %v\n", data)
    })

    // 开始监听（阻塞）
    cli.Listen()
}
```

## 协议说明

### 认证流程

如果服务器设置了 `SecretKey`，客户端连接后需要先进行认证：

1. 客户端发送认证请求：
```json
{
  "token": "sha1(secretKey + pluginName)",
  "plugin_name": "my-plugin"
}
```

2. 服务器响应：
```json
{
  "status": "authenticated",
  "message": "认证成功"
}
```

### JSON-RPC 方法

#### subscribe - 订阅事件

**请求:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "subscribe",
  "params": {
    "event": "user_login"
  }
}
```

**响应:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "status": "subscribed",
    "event": "user_login",
    "message": "成功订阅 user_login 事件"
  }
}
```

#### unsubscribe - 取消订阅

**请求:**
```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "unsubscribe",
  "params": {
    "event": "user_login"
  }
}
```

### 事件消息格式

服务器发送的事件消息格式：

```json
{
  "event": "user_login",
  "data": {
    "user_id": 12345,
    "username": "john_doe"
  }
}
```

## API 文档

### 服务端 (server 包)

#### `NewServer(config Config) *Server`

创建新的 WebSocket 事件服务器。

**Config 字段:**
- `Port string`: 监听端口
- `SecretKey string`: 认证密钥（可选，如果为空则不进行认证）
- `Logger Logger`: 日志记录器（可选）

#### `Server.Start() error`

启动服务器。

#### `Server.Stop() error`

停止服务器，关闭所有连接。

#### `Server.Emit(event string, data interface{}) error`

触发事件，向所有订阅了该事件的客户端广播。

#### `Server.GetClientCount() int`

获取当前连接的客户端数量。

### 客户端 (client 包)

#### `NewClient(config Config) (*Client, error)`

创建新的 WebSocket 事件客户端。

**Config 字段:**
- `Address string`: 服务器地址，格式: `"127.0.0.1:8085"` 或 `"ws://localhost:8085"`
- `PluginName string`: 插件名称（用于认证）
- `SecretKey string`: 认证密钥（如果服务器需要认证）
- `Logger Logger`: 日志记录器（可选）

#### `Client.Connect() error`

连接到服务器。

#### `Client.On(eventName string, handler EventHandler)`

订阅事件，注册事件处理函数。

#### `Client.Off(eventName string)`

取消订阅事件。

#### `Client.Listen() error`

开始监听事件（阻塞调用）。

#### `Client.Stop()`

停止监听，关闭连接。

#### `Client.IsConnected() bool`

检查是否已连接到服务器。

## 其他语言客户端实现

由于使用标准的 WebSocket 和 JSON-RPC 协议，任何支持 WebSocket 的语言都可以实现客户端。

### Python 示例

```python
import asyncio
import websockets
import json
import hashlib

async def client():
    uri = "ws://127.0.0.1:8085"
    async with websockets.connect(uri) as websocket:
        # 认证
        secret_key = "my-secret-key"
        plugin_name = "my-plugin"
        token = hashlib.sha1((secret_key + plugin_name).encode()).hexdigest()
        
        auth = {
            "token": token,
            "plugin_name": plugin_name
        }
        await websocket.send(json.dumps(auth))
        response = await websocket.recv()
        print(f"认证响应: {response}")
        
        # 订阅事件
        subscribe = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "subscribe",
            "params": {"event": "user_login"}
        }
        await websocket.send(json.dumps(subscribe))
        
        # 监听事件
        while True:
            message = await websocket.recv()
            data = json.loads(message)
            if "event" in data:
                print(f"收到事件: {data['event']}, 数据: {data['data']}")

asyncio.run(client())
```

### JavaScript 示例

```javascript
const WebSocket = require('ws');
const crypto = require('crypto');

const ws = new WebSocket('ws://127.0.0.1:8085');

ws.on('open', () => {
  // 认证
  const secretKey = 'my-secret-key';
  const pluginName = 'my-plugin';
  const token = crypto.createHash('sha1')
    .update(secretKey + pluginName)
    .digest('hex');
  
  ws.send(JSON.stringify({
    token: token,
    plugin_name: pluginName
  }));
});

ws.on('message', (data) => {
  const message = JSON.parse(data);
  
  if (message.status === 'authenticated') {
    // 订阅事件
    ws.send(JSON.stringify({
      jsonrpc: '2.0',
      id: 1,
      method: 'subscribe',
      params: { event: 'user_login' }
    }));
  } else if (message.event) {
    // 处理事件
    console.log('收到事件:', message.event, message.data);
  }
});
```

## 示例代码

完整示例代码请参考 `examples` 目录：

- `examples/server/main.go` - 服务端示例
- `examples/client/main.go` - 客户端示例

运行示例：

```bash
# 终端 1: 启动服务端
cd examples/server
go run main.go

# 终端 2: 启动客户端
cd examples/client
go run main.go
```

## 许可证

MIT License

## 贡献

欢迎提交 Issue 和 Pull Request！

