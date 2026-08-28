package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// render writes an API response body to w in the requested format. "json"
// (default) pretty-prints; "yaml" emits YAML; "table" renders a JSON array of
// objects as a column table, falling back to pretty JSON for any non-array body.
func render(w io.Writer, raw json.RawMessage, format string) error {
	if len(raw) == 0 {
		return nil
	}
	switch format {
	case "table":
		if ok, err := renderTable(w, raw); ok || err != nil {
			return err
		}
		return renderJSON(w, raw)
	case "yaml":
		return renderYAML(w, raw)
	default:
		return renderJSON(w, raw)
	}
}

func renderYAML(w io.Writer, raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Not JSON (e.g. a plain string body) — emit verbatim.
		_, err := fmt.Fprintln(w, strings.TrimSpace(string(raw)))
		return err
	}
	out, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode yaml: %w", err)
	}
	_, err = w.Write(out)
	return err
}

func renderJSON(w io.Writer, raw json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		// Not JSON (e.g. a plain string body) — emit verbatim.
		_, err := fmt.Fprintln(w, strings.TrimSpace(string(raw)))
		return err
	}
	_, err := fmt.Fprintln(w, string(decodeHTMLEscapes(buf.Bytes())))
	return err
}

// decodeHTMLEscapes rewrites the unicode escapes that encoding/json emits by
// default for ampersand, less-than, and greater-than back to their literal
// characters. The OpenTAMS server escapes this way, so the ampersands in a
// presigned URL's signed query string arrive as their six-character escape
// form — valid JSON that every parser decodes, but awkward to copy-paste from
// raw output. renderYAML/renderTable already decode via Unmarshal; this brings
// the json path in line. Only genuine escapes are rewritten: an escaped
// backslash is copied as a pair so a backslash-u sequence that is part of
// string content (not an escape) is left intact rather than corrupted.
func decodeHTMLEscapes(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if b[i] == '\\' && i+1 < len(b) && b[i+1] == '\\' {
			out = append(out, '\\', '\\')
			i += 2
			continue
		}
		if b[i] == '\\' && i+6 <= len(b) && b[i+1] == 'u' {
			switch string(b[i+2 : i+6]) {
			case "0026":
				out = append(out, '&')
				i += 6
				continue
			case "003c", "003C":
				out = append(out, '<')
				i += 6
				continue
			case "003e", "003E":
				out = append(out, '>')
				i += 6
				continue
			}
		}
		out = append(out, b[i])
		i++
	}
	return out
}

// renderTable renders raw as a table if it is a JSON array of objects.
// Returns ok=false (without writing) when raw is not array-of-objects so the
// caller can fall back to JSON.
func renderTable(w io.Writer, raw json.RawMessage) (bool, error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return false, nil //nolint:nilerr // not an array-of-objects; signal fallback.
	}
	if len(rows) == 0 {
		return false, nil
	}

	cols := orderedColumns(rows)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(upper(cols), "\t")); err != nil {
		return false, err
	}
	for _, row := range rows {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = cell(row[c])
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return false, err
		}
	}
	return true, tw.Flush()
}

// orderedColumns picks table columns: a few well-known fields first (in this
// order if present), then any remaining keys from the first row, sorted.
func orderedColumns(rows []map[string]json.RawMessage) []string {
	priority := []string{"id", "object_id", "source_id", "flow_id", "label", "format", "codec", "timerange"}
	seen := map[string]bool{}
	var cols []string
	for _, p := range priority {
		if _, ok := rows[0][p]; ok {
			cols = append(cols, p)
			seen[p] = true
		}
	}
	var rest []string
	for k := range rows[0] {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(cols, rest...)
}

// cell renders a single JSON value for a table cell: strings unquoted,
// everything else compact JSON.
func cell(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s
	}
	return string(v)
}

func upper(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = strings.ToUpper(c)
	}
	return out
}
