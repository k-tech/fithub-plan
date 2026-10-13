package main

import (
	"bufio"
	"context"
	"io"
	"unicode/utf8"
)

// streamLines reads output line by line and forwards each line to the log sink
// until the reader ends or the context is cancelled. It bounds one line's length
// so a program that never emits a newline cannot make the agent buffer forever.
func streamLines(ctx context.Context, source io.Reader, sink func(string)) {
	reader := bufio.NewReaderSize(source, 32<<10)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line, err := reader.ReadString('\n')
		if line != "" {
			// Invalid UTF-8 and control characters would corrupt the log view, so
			// they are replaced and stripped before upload.
			sink(sanitizeLogLine(line))
		}
		if err != nil {
			return
		}
	}
}

// maxLogLineBytes bounds one emitted line. A longer line is truncated rather than
// buffered, and the truncation is marked so a reader knows output was cut.
const maxLogLineBytes = 8 << 10

// sanitizeLogLine makes a line safe to store and display: valid UTF-8, no
// terminal control sequences other than a trailing newline, and a bounded length.
func sanitizeLogLine(line string) string {
	if len(line) > maxLogLineBytes {
		line = line[:maxLogLineBytes] + "…(line truncated)\n"
	}
	line = toValidUTF8(line)
	var builder []rune
	for _, char := range line {
		switch {
		case char == '\n' || char == '\t':
			builder = append(builder, char)
		case char < 0x20 || char == 0x7f:
			// Drop other control characters so an escape sequence cannot rewrite
			// a terminal that later displays the log.
		default:
			builder = append(builder, char)
		}
	}
	return string(builder)
}

// toValidUTF8 replaces invalid sequences so the stored log is always valid text.
func toValidUTF8(value string) string {
	if utf8.ValidString(value) {
		return value
	}
	out := make([]rune, 0, len(value))
	for len(value) > 0 {
		char, size := utf8.DecodeRuneInString(value)
		if char == utf8.RuneError && size == 1 {
			out = append(out, utf8.RuneError)
		} else {
			out = append(out, char)
		}
		value = value[size:]
	}
	return string(out)
}
