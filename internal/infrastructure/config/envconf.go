// env.conf 支持：${home}/.laxcode/env.conf 每行一条 k=v，进程启动早期注入
// os 环境，作为 bash 工具与 MCP server（stdio 派生进程与 Streamable HTTP
// 的代理环境）的统一环境基线。解析与本包其余 viper 装配相互独立：格式刻意
// 保持最简（无变量展开、无 unset），错误一律 fail-fast 并携带行号。

package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

// envKeyPattern 校验环境变量名：字母或下划线开头，只含字母、数字与下划线。
// env.conf 是进程环境基线，非法名字直接拒绝而非静默跳过，避免"配置了代理
// 却不生效"这类难以排查的问题。
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// parseEnvConf 把 env.conf 字节内容解析为有序的 "K=V" 切片。规则：
//   - 每行一条 KEY=VALUE；# 注释与空行跳过，容忍 CRLF；
//   - 容忍 dotenv 惯例：可选的 "export " 前缀、值两侧成对的单/双引号；
//   - 重复 key 后者覆盖且保持首次出现的位置；
//   - 不做变量展开、不支持 unset；
//   - 任何缺少 "=" 或 key 非法的行都是错误，携带行号。
//
// 返回有序切片而非 map：调用方按顺序 Setenv / 追加 cmd.Env，覆盖顺序必须
// 确定。value 内的 "=" 原样保留（如 pg 的 CONNECTION=host=db port=5432）。
func parseEnvConf(data []byte) ([]string, error) {
	var entries []string
	seen := make(map[string]int)
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("env.conf 第 %d 行缺少 \"=\"：%q", i+1, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !envKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("env.conf 第 %d 行环境变量名非法：%q", i+1, key)
		}
		value = stripEnvQuotes(value)
		if idx, ok := seen[key]; ok {
			entries[idx] = key + "=" + value
			continue
		}
		seen[key] = len(entries)
		entries = append(entries, key+"="+value)
	}
	return entries, nil
}

// stripEnvQuotes 剥离值两侧成对的单引号或双引号（dotenv 惯例）；不成对则
// 原样保留。
func stripEnvQuotes(value string) string {
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}

// ApplyEnvFile 读取 ${home}/.laxcode/env.conf 并把其中的 k=v 逐条写入进程
// 环境（os.Setenv，覆盖启动 shell 的继承值）。文件不存在是完全合法的
// no-op；settings.json 的 mcp_servers.<name>.env 在派生 MCP stdio 进程时
// 追加在本环境之后，优先级更高。
//
// 必须在进程首次出站 HTTP 请求之前调用：net/http 的 ProxyFromEnvironment
// 经 sync.Once 在首次请求时缓存代理环境，之后注入 HTTP(S)_PROXY 不再生效。
// 当前唯一调用点是 ParseEnvAndFile（main 最早期）。第三方包的初始化可能
// 更早缓存代理环境；ChatGPT 使用独立 transport 读取此处注入后的配置。
func ApplyEnvFile(homeDir string) error {
	data, err := os.ReadFile(layout.EnvConf(homeDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取 env.conf 失败: %w", err)
	}
	entries, err := parseEnvConf(data)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("注入环境变量 %s 失败: %w", key, err)
		}
	}
	return nil
}
