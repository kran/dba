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

// LogHook returns a middleware that logs every SQL execution with duration,
// query, and arguments. Queries exceeding slowThreshold are logged at Warn
// level. Set cleanSpec to true to fold whitespace for single-line display.
// SQL 语法 (注释/方言) 原样保留 —— dba 不解释 SQL。
func LogHook(logger *slog.Logger, slowThreshold time.Duration, cleanSpec bool) Hook {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next ExecFunc) ExecFunc {
		return func(ctx context.Context, query string, args []any) (any, error) {
			start := time.Now()
			result, err := next(ctx, query, args)
			duration := time.Since(start)

			cleaned := query
			if cleanSpec {
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

			return result, err
		}
	}
}
