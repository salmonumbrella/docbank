package mcp

import (
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestCheckAnswerNormalizationSchema(t *testing.T) {
	_, output := checkAnswerSchemas()
	figures := checkAnswerFigureProperties(t, output)
	require.Contains(t, objectField(t, figures, "kind")["enum"], "date")
	require.Contains(t, objectField(t, figures, "kind")["enum"], "amount")
	require.Contains(t, objectField(t, figures, "status")["enum"], "value_only")
}

func TestCheckAnswerNormalizedFiguresThroughTransports(t *testing.T) {
	text := "March 15, 2026 costs USD 40"
	citation := citationMCPArguments()
	citation["end"] = utf8.RuneCountInString(text)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{"citation": citation, "text": text, "text_bytes": len(text), "text_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(text)))})
	}))
	defer daemon.Close()
	server := newServerWithOptionsAndDaemon(testImplementation(), ServerOptions{}, exportTestLease(t, daemon.URL))
	for _, transport := range []string{"stdio", "http"} {
		for _, tc := range []struct{ answer, status, figureStatus string }{{"2026-03-15 costs $40 [1]", "grounded", "found"}, {"2026-03-15 costs €40 [1]", "ungrounded", "value_only"}} {
			result := decodeResult(t, exportExchange(t, server, transport, "tools/call", map[string]any{"name": "check_answer", "arguments": map[string]any{"answer": tc.answer, "citations": []any{citation}}}))
			output := decodeCheckAnswerOutput(t, result)
			require.Equal(t, tc.status, output.Status)
			require.Len(t, output.Claims, 1)
			figures := output.Claims[0].Figures
			require.Len(t, figures, 2)
			require.Equal(t, "date", figures[0].Kind)
			require.Equal(t, "amount", figures[1].Kind)
			require.Equal(t, tc.figureStatus, figures[1].Status)
		}
	}
}

func TestCheckAnswerScaledAmountEnvelope(t *testing.T) {
	text := "$9"
	citation := citationMCPArguments()
	citation["end"] = len(text)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{"citation": citation, "text": text, "text_bytes": len(text), "text_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(text)))})
	}))
	defer daemon.Close()
	server := newServerWithOptionsAndDaemon(testImplementation(), ServerOptions{}, exportTestLease(t, daemon.URL))
	answer := "$" + strings.Repeat("9", maxRenditionChars-3) + "bn"
	result := exportExchange(t, server, "stdio", "tools/call", map[string]any{"name": "check_answer", "arguments": map[string]any{"answer": answer, "citations": []any{citation}}})
	require.Less(t, len(result), maxToolResponseBytes)
	output := decodeCheckAnswerOutput(t, decodeResult(t, result))
	require.Len(t, output.Claims, 1)
	require.Len(t, output.Claims[0].Figures, 1)
	figure := output.Claims[0].Figures[0]
	require.Equal(t, "amount", figure.Kind)
	require.Len(t, figure.Values, 1)
	value := figure.Values[0]
	require.Equal(t, "$ "+strings.Repeat("9", maxRenditionChars-3)+"000000000", value)
	_, schema := checkAnswerSchemas()
	properties := checkAnswerFigureProperties(t, schema)
	valueSchema := objectField(t, objectField(t, properties, "values"), "items")
	maxLength, ok := valueSchema["maxLength"].(int)
	require.True(t, ok, "maxLength is %T", valueSchema["maxLength"])
	require.LessOrEqual(t, utf8.RuneCountInString(value), maxLength)
}

func checkAnswerFigureProperties(t *testing.T, output schema) schema {
	t.Helper()
	for _, key := range []string{"properties", "claims", "items", "properties", "figures", "items", "properties"} {
		output = objectField(t, output, key)
	}
	return output
}

func decodeCheckAnswerOutput(t *testing.T, result map[string]any) checkAnswerOutput {
	t.Helper()
	data, err := json.Marshal(objectField(t, result, "structuredContent"))
	require.NoError(t, err)
	var output checkAnswerOutput
	require.NoError(t, json.Unmarshal(data, &output))
	return output
}
