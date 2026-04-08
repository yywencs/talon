package main

import (
	"log"

	"github.com/gin-gonic/gin"
)

func main() {
	gin.SetMode(gin.DebugMode)

	r := gin.Default()

	// 注册一个基础的健康检查接口，用于后续 K8s 部署探针
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "up",
			"engine": "agentflow-core",
		})
	})

	// 预留的流式对话接口入口（待实现）
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		c.String(200, "流式网关入口已就绪\n")
	})

	log.Println("AgentFlow Engine is starting on port 8080...")

	// 启动服务
	if err := r.Run(":8080"); err != nil {
		log.Fatalf("Server startup failed: %v", err)
	}
}
