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

// LogHook 返回一个日志中间件，记录每次 SQL 执行的耗时、查询和参数
// slowThreshold > 0 时，超过阈值的查询会以 Warn 级别记录
func LogHook(logger *slog.Logger, slowThreshold time.Duration) Hook {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next ExecFunc) ExecFunc {
		return func(ctx context.Context, query string, args []any) (any, error) {
			start := time.Now()
			result, err := next(ctx, query, args)
			duration := time.Since(start)

			cleaned := cleanSQL(query)
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

			return result, err
		}
	}
}

func cleanSQL(sql string) string {
	if sql == "" {
		return ""
	}

	length := len(sql)
	// 预估容量，避免频繁扩容
	buf := make([]byte, 0, length)
	inString := false

	// 辅助函数：如果缓冲区最后一个字符不是空格，则添加一个空格
	appendSpaceIfNecessary := func() {
		if len(buf) > 0 && buf[len(buf)-1] != ' ' {
			buf = append(buf, ' ')
		}
	}

	for i := 0; i < length; i++ {
		c := sql[i]

		if inString && c == '\\' {
			// 处理字符串内的转义字符，如 \'
			buf = append(buf, c)
			if i+1 < length {
				i++
				buf = append(buf, sql[i])
			}
		} else if c == '\'' {
			// 切换字符串内/外状态
			inString = !inString
			buf = append(buf, c)
		} else if inString {
			// 字符串内的字符原样保留
			buf = append(buf, c)
		} else if c == '/' && i+1 < length && sql[i+1] == '*' {
			// 跳过块注释 /* ... */
			for i += 2; i < length-1; i++ {
				if sql[i] == '*' && sql[i+1] == '/' {
					i++ // 跳过 '/'
					break
				}
			}
			appendSpaceIfNecessary()
		} else {
			// 检查是否是单行注释: "-- " (带空格) 或者是位于结尾的 "--"
			isDashComment := c == '-' && i+1 < length && sql[i+1] == '-' &&
				(i+2 >= length || isWhitespace(sql[i+2]))

			if c == '#' || isDashComment {
				// 跳过单行注释，直到遇到换行符
				for i < length && sql[i] != '\n' && sql[i] != '\r' {
					i++
				}
				appendSpaceIfNecessary()
			} else {
				if isWhitespace(c) {
					// 将任何空白字符压缩为一个空格
					appendSpaceIfNecessary()
				} else {
					// 普通字符直接追加
					buf = append(buf, c)
				}
			}
		}
	}

	// 转换回字符串并去除首尾多余空格
	return strings.TrimSpace(string(buf))
}

// 辅助函数：判断是否是空白字符
func isWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
