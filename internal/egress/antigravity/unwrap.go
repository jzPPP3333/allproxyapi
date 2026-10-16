package antigravity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// Unwrap strips the Cloud Code PA {"response": ...} envelope from an SSE body,
// returning a reader over the plain Gemini SSE stream.
func Unwrap(src io.Reader) (io.Reader, error) {
	pr, pw := io.Pipe()
	go func() {
		err := UnwrapStreamPipe(src, pw)
		_ = pw.CloseWithError(err)
	}()
	return pr, nil
}

// UnwrapJSON extracts the inner "response" object if present, or returns the original JSON.
func UnwrapJSON(raw []byte) ([]byte, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return raw, nil
	}
	if inner, ok := env["response"]; ok && len(inner) > 0 {
		return inner, nil
	}
	return raw, nil
}

// UnwrapStreamPipe reads an SSE stream from src, unwraps any {"response": ...} payloads in "data:" lines,
// and writes the unwrapped SSE stream to dst.
func UnwrapStreamPipe(src io.Reader, dst io.Writer) error {
	scanner := bufio.NewScanner(src)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024) // up to 1MB line buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		trimmed := bytes.TrimSpace(line)

		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			// Write unmodified non-data line (e.g. empty line or event:)
			if _, err := dst.Write(line); err != nil {
				return err
			}
			if _, err := dst.Write([]byte("\n")); err != nil {
				return err
			}
			continue
		}

		payload := bytes.TrimSpace(trimmed[5:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			if _, err := dst.Write(line); err != nil {
				return err
			}
			if _, err := dst.Write([]byte("\n")); err != nil {
				return err
			}
			continue
		}

		unwrapped, err := UnwrapJSON(payload)
		if err != nil {
			unwrapped = payload
		}

		if _, err := dst.Write([]byte("data: ")); err != nil {
			return err
		}
		if _, err := dst.Write(unwrapped); err != nil {
			return err
		}
		if _, err := dst.Write([]byte("\n")); err != nil {
			return err
		}
	}

	return scanner.Err()
}
