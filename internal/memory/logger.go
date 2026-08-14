package memory

import (
	"log"
	"time"

	"gorm.io/gorm/logger"
)

// gormLogger 静音 GORM 的 SQL 日志（只保留慢查询与错误，写 stderr）。
var gormLogger = logger.New(
	log.New(log.Writer(), "[memory] ", log.LstdFlags),
	logger.Config{
		SlowThreshold:             500 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
	},
)

// logPrintf 统一日志出口（writeLoop 落库失败用）。
func logPrintf(format string, args ...any) {
	log.Printf("[memory] "+format, args...)
}
