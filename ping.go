package main

import (
	"encoding/json"
	"net/http"
	"time"
)

// Response 定义统一的响应结构
type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// pingHandler 处理 ping 请求
func pingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Response{
		Code:    0,
		Message: "pong",
	})
}

// main 启动 HTTP 服务
func main() {
	http.HandleFunc("/ping", pingHandler)
	server := &http.Server{
		Addr:         ":8080",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	server.ListenAndServe()
}
