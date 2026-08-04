package dba

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// NewLogger 默认执行日志回调 (基于 slog): 慢查询 Warn / 错误 Error / 常规 Debug。
// Set cleanSpec to true to fold whitespace for single-line display.
// SQL 语法 (注释/方言) 原样保留 —— dba 不解释 SQL。
func NewLogger(logger *slog.Logger, slowThreshold time.Duration, doClean bool) LogFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, begin time.Time, query string, args []any, err error) {
		duration := time.Since(begin)

		cleaned := query
		if doClean {
			cleaned = strings.Join(strings.Fields(query), " ")
		}
		attrs := []slog.Attr{
			slog.Duration("duration", duration),
			slog.String("sql", cleaned),
		}
		if len(args) > 0 {
			attrs = append(attrs, slog.String("args", fmt.Sprintf("%v", args)))
		}

		if err != nil {
			level := slog.LevelError
			if errors.Is(err, sql.ErrNoRows) {
				level = slog.LevelDebug
			}
			attrs = append(attrs, slog.String("error", err.Error()))
			logger.LogAttrs(ctx, level, "SQL", attrs...)
		} else if slowThreshold > 0 && duration >= slowThreshold {
			logger.LogAttrs(ctx, slog.LevelWarn, "SLOW SQL", attrs...)
		} else {
			logger.LogAttrs(ctx, slog.LevelDebug, "SQL", attrs...)
		}
	}
}
