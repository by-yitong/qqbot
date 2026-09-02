package utils

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvOr 读环境变量，为空则返回默认值。
func EnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// EnvInt 读环境变量为整数，为空或非法则返回默认值。
func EnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// EnvDurationSec 读环境变量为「秒」并转 time.Duration；<=0 返回 0（让调用方用默认值）。
func EnvDurationSec(key string, def int) time.Duration {
	n := EnvInt(key, def)
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// SplitList 把逗号分隔的字符串解析成列表（用于白名单等，空项跳过）。
func SplitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
