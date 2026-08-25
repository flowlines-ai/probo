// Copyright (c) 2025-2026 Probo Inc <hello@probo.com>.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package mcputils_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	apiTrace "go.opentelemetry.io/otel/trace"
	"go.probo.inc/probo/pkg/server/api/mcp/mcputils"
)

const secretSentinel = "secret-backend-message-7f34"

func newFlowlinesRecorder(t *testing.T) (apiTrace.Tracer, *tracetest.SpanRecorder) {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := trace.NewTracerProvider(trace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	return provider.Tracer("flowlines-test"), recorder
}

func flowlinesRequest(arguments map[string]any) *mcp.CallToolRequest {
	payload, err := json.Marshal(arguments)
	if err != nil {
		panic(err)
	}

	return &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Meta: mcp.Meta{
				"session.id":    "session-42",
				"user.id":       "spoofed-user",
				"authorization": secretSentinel,
			},
			Name:      "get_order",
			Arguments: payload,
		},
		Extra: &mcp.RequestExtra{
			Header: http.Header{
				"Authorization": {"Bearer " + secretSentinel},
				"User-Agent":    {"flowlines-test-client/1.0"},
			},
		},
	}
}

func spanAttributes(span trace.ReadOnlySpan) map[string]any {
	attributes := make(map[string]any)
	for _, attr := range span.Attributes() {
		attributes[string(attr.Key)] = attr.Value.AsInterface()
	}

	return attributes
}

func TestFlowlinesMiddleware_Success(t *testing.T) {
	t.Parallel()

	tracer, recorder := newFlowlinesRecorder(t)
	req := flowlinesRequest(
		map[string]any{
			"reason":      "Check whether the order shipped",
			"user_intent": "Get order 123 delivered",
			"order_id":    "order-123",
		},
	)
	wantResult := &mcp.CallToolResult{
		StructuredContent: map[string]any{"state": "shipped"},
	}
	handler := mcputils.FlowlinesMiddleware(
		tracer,
		func(context.Context) string { return "verified-user" },
	)(
		func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return wantResult, nil
		},
	)

	result, err := handler(context.Background(), "tools/call", req)

	require.NoError(t, err)
	assert.Same(t, wantResult, result)
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	span := spans[0]
	attrs := spanAttributes(span)
	assert.Equal(t, "execute_tool get_order", span.Name())
	assert.Equal(t, apiTrace.SpanKindServer, span.SpanKind())
	assert.Equal(t, codes.Ok, span.Status().Code)
	assert.Equal(t, "execute_tool", attrs["gen_ai.operation.name"])
	assert.Equal(t, "get_order", attrs["gen_ai.tool.name"])
	assert.Equal(t, "Check whether the order shipped", attrs["gen_ai.tool.call.reason"])
	assert.Equal(t, "Get order 123 delivered", attrs["session.user_intent"])
	assert.Equal(t, "tools/call", attrs["mcp.method.name"])
	assert.Equal(t, "probo", attrs["mcp.server.name"])
	assert.NotEmpty(t, attrs["gen_ai.tool.call.id"])
	assert.Equal(t, "session-42", attrs["session.id"])
	assert.Equal(t, "verified-user", attrs["user.id"])
	assert.Equal(t, "flowlines-test-client/1.0", attrs["user_agent.original"])
	assert.JSONEq(
		t,
		`{"order_id":"order-123","reason":"Check whether the order shipped","user_intent":"Get order 123 delivered"}`,
		attrs["gen_ai.tool.call.arguments"].(string),
	)
	assert.Contains(t, attrs["gen_ai.tool.call.result"], "shipped")
	assert.NotContains(t, attrs["gen_ai.tool.call.arguments"], "_meta")
	assert.NotContains(t, attrs["gen_ai.tool.call.arguments"], secretSentinel)
	assert.NotContains(t, attrs["gen_ai.tool.call.result"], secretSentinel)
	assert.Empty(t, span.Events())
}

func TestFlowlinesMiddleware_DistinctCallIDs(t *testing.T) {
	t.Parallel()

	tracer, recorder := newFlowlinesRecorder(t)
	req := flowlinesRequest(
		map[string]any{
			"reason":      "Read the same order again",
			"user_intent": "Get order 123 delivered",
		},
	)
	handler := mcputils.FlowlinesMiddleware(tracer, nil)(
		func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return &mcp.CallToolResult{}, nil
		},
	)

	_, err := handler(context.Background(), "tools/call", req)
	require.NoError(t, err)
	_, err = handler(context.Background(), "tools/call", req)
	require.NoError(t, err)

	spans := recorder.Ended()
	require.Len(t, spans, 2)
	firstID := spanAttributes(spans[0])["gen_ai.tool.call.id"]
	secondID := spanAttributes(spans[1])["gen_ai.tool.call.id"]
	assert.NotEqual(t, firstID, secondID)
}

func TestFlowlinesMiddleware_ParentTraceAndPayloadBound(t *testing.T) {
	t.Parallel()

	tracer, recorder := newFlowlinesRecorder(t)
	ctx, parent := tracer.Start(context.Background(), "parent")
	parentSpanID := parent.SpanContext().SpanID()
	req := flowlinesRequest(
		map[string]any{
			"reason":      "Read a large generated report",
			"user_intent": "Review the generated report",
		},
	)
	handler := mcputils.FlowlinesMiddleware(tracer, nil)(
		func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			return &mcp.CallToolResult{
				StructuredContent: map[string]any{"report": strings.Repeat("x", 60_000)},
			}, nil
		},
	)

	_, err := handler(ctx, "tools/call", req)
	parent.End()

	require.NoError(t, err)
	spans := recorder.Ended()
	require.Len(t, spans, 2)
	child := spans[0]
	assert.Equal(t, parentSpanID, child.Parent().SpanID())
	attrs := spanAttributes(child)
	var boundedResult struct {
		Truncated    bool `json:"truncated"`
		OriginalSize int  `json:"original_size"`
	}
	require.NoError(t, json.Unmarshal([]byte(attrs["gen_ai.tool.call.result"].(string)), &boundedResult))
	assert.True(t, boundedResult.Truncated)
	assert.Greater(t, boundedResult.OriginalSize, 50_000)
}

func TestFlowlinesMiddleware_SafeToolError(t *testing.T) {
	t.Parallel()

	tracer, recorder := newFlowlinesRecorder(t)
	req := flowlinesRequest(
		map[string]any{
			"reason":      "Try to read an inaccessible order",
			"user_intent": "Get order 123 delivered",
		},
	)
	handler := mcputils.FlowlinesMiddleware(tracer, nil)(
		func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			publicMCPError := func(error) *mcp.CallToolResult {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: "request could not be completed"},
					},
					IsError: true,
				}
			}

			return publicMCPError(errors.New(secretSentinel)), nil
		},
	)

	result, err := handler(context.Background(), "tools/call", req)

	require.NoError(t, err)
	require.NotNil(t, result)
	spans := recorder.Ended()
	require.Len(t, spans, 1)
	span := spans[0]
	attrs := spanAttributes(span)
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Equal(t, "mcp_tool_error", attrs["error.type"])
	assert.Contains(t, attrs["gen_ai.tool.call.result"], "request could not be completed")
	for key := range attrs {
		assert.NotContains(t, key, "authorization")
	}
	encodedAttrs, err := json.Marshal(attrs)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedAttrs), secretSentinel)
	assert.Empty(t, span.Events())
}

func TestFlowlinesMiddleware_InvalidFlowlinesFieldsAreNotExported(t *testing.T) {
	t.Parallel()

	tracer, recorder := newFlowlinesRecorder(t)
	req := flowlinesRequest(
		map[string]any{
			"reason":      "   ",
			"user_intent": "Get order 123 delivered",
		},
	)
	nextCalled := false
	handler := mcputils.FlowlinesMiddleware(tracer, nil)(
		func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
			nextCalled = true
			return &mcp.CallToolResult{IsError: true}, nil
		},
	)

	_, err := handler(context.Background(), "tools/call", req)

	require.NoError(t, err)
	assert.True(t, nextCalled)
	assert.Empty(t, recorder.Ended())
}

func TestFlowlinesInstructionsMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		result       mcp.Result
		instructions func(mcp.Result) string
	}{
		{
			name:   "initialize",
			result: &mcp.InitializeResult{},
			instructions: func(result mcp.Result) string {
				return result.(*mcp.InitializeResult).Instructions
			},
		},
		{
			name:   "discover",
			result: &mcp.DiscoverResult{},
			instructions: func(result mcp.Result) string {
				return result.(*mcp.DiscoverResult).Instructions
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				t.Parallel()

				handler := mcputils.FlowlinesInstructionsMiddleware()(
					func(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
						return tt.result, nil
					},
				)
				result, err := handler(context.Background(), tt.name, nil)

				require.NoError(t, err)
				instructions := tt.instructions(result)
				assert.Contains(t, instructions, "reason")
				assert.Contains(t, instructions, "user_intent")
				assert.Contains(t, instructions, "report_outcome")
				assert.Contains(t, instructions, "last tool call")
			},
		)
	}
}
