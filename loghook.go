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
// level. Set cleanSpec to true to strip comments and normalize whitespace.
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
				cleaned = cleanSQL(query)
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

func cleanSQL(sql string) string {
	if sql == "" {
		return ""
	}

	length := len(sql)
	buf := make([]byte, 0, length)
	inString := false

	appendSpaceIfNecessary := func() {
		if len(buf) > 0 && buf[len(buf)-1] != ' ' {
			buf = append(buf, ' ')
		}
	}

	for i := 0; i < length; i++ {
		c := sql[i]

		if inString && c == '\\' {
			buf = append(buf, c)
			if i+1 < length {
				i++
				buf = append(buf, sql[i])
			}
		} else if c == '\'' {
			inString = !inString
			buf = append(buf, c)
		} else if inString {
			buf = append(buf, c)
		} else if c == '/' && i+1 < length && sql[i+1] == '*' {
			for i += 2; i < length-1; i++ {
				if sql[i] == '*' && sql[i+1] == '/' {
					i++
					break
				}
			}
			appendSpaceIfNecessary()
		} else {
			isDashComment := c == '-' && i+1 < length && sql[i+1] == '-' &&
				(i+2 >= length || isWhitespace(sql[i+2]))

			if c == '#' || isDashComment {
				for i < length && sql[i] != '\n' && sql[i] != '\r' {
					i++
				}
				appendSpaceIfNecessary()
			} else {
				if isWhitespace(c) {
					appendSpaceIfNecessary()
				} else {
					buf = append(buf, c)
				}
			}
		}
	}

	return strings.TrimSpace(string(buf))
}

func isWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
