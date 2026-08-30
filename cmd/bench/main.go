// cmd/bench/main.go
// 自动化 Benchmark 跑分入口。
//
// 跑法：go run cmd/bench/main.go
// 前置：export MINIMAX_API_KEY=<your-key>
package main

import (
	"context"
	"log"
	"os"

	"github.com/BillZong/go-tiny-claw/internal/eval"
)

func main() {
	if os.Getenv("MINIMAX_API_KEY") == "" {
		log.Fatal("请先导出 MINIMAX_API_KEY 环境变量进行跑分测试")
	}

	// 构建一套微型评测集：
	//   1. test_001_edit        —— 考察 edit_file 工具的模糊替换稳定性
	//   2. test_002_code_gen    —— 考察 Agent 阅读代码 + 写新文件的综合能力
	//
	// 说明：test_002 的 SetupScript 使用 printf 而非 echo，
	// 避免 echo 在不同 shell (zsh / Windows cmd) 下的转义差异导致多行内容写入异常。
	testcases := []eval.TestCase{
		{
			ID:   "test_001_edit",
			Name: "测试模糊替换工具的准确性",
			// 准备靶机：生成一个有初始版本号的 json 文件
			SetupScript: `printf '{"name": "tiny-claw", "version": "v1.0.0"}\n' > config.json`,
			// 考题：要求修改版本号
			TaskPrompt: `当前目录下有一个 config.json。请你使用 edit_file 工具，将其中的 version 从 v1.0.0 改为 v2.0.0。不要做其他多余操作。`,
			// 判卷脚本：使用 grep 检查文件是否包含 v2.0.0
			ValidateScript: `grep '"version": "v2.0.0"' config.json`,
		},
		{
			ID:   "test_002_code_gen",
			Name: "测试代码阅读与创建新文件的综合能力",
			// 准备靶机：生成一个简单的乘法函数
			SetupScript: `printf 'package math\n\nfunc Multiply(a, b int) int {\n\treturn a * b\n}\n' > math.go`,
			// 考题：要求 Agent 根据刚才的代码，自己去写一份单元测试
			TaskPrompt: `当前目录下有一个 math.go。请你仔细阅读它，然后在同级目录下，帮我写一个规范的单元测试文件 math_test.go，用来测试 Multiply 函数。请务必包含正常的测试用例。`,
			// 判卷脚本：直接运行 go test！如果不通过则直接 0 分
			ValidateScript: `go mod init bench && go test -v ./...`,
		},
	}

	// 启动跑分执行器
	runner := eval.NewBenchmarkRunner("MiniMax-M3")
	runner.RunSuite(context.Background(), testcases)
}
