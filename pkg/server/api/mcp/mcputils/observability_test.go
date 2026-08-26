// Copyright (c) 2026 Probo Inc <hello@probo.com>.
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

package mcputils

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	mcpgenmcp "go.probo.inc/mcpgen/mcp"
	"go.probo.inc/probo/pkg/coredata"
	"go.probo.inc/probo/pkg/gid"
	"go.probo.inc/probo/pkg/mail"
	"go.probo.inc/probo/pkg/server/api/authn"
)

const flowlinesTestSchema = `{
  "type": "object",
  "properties": {
    "reason": {"type": "string", "minLength": 1, "pattern": ".*\\S.*"},
    "user_intent": {"type": "string", "minLength": 1, "pattern": ".*\\S.*"},
    "priority": {"type": "string", "default": "MEDIUM"}
  },
  "required": ["reason", "user_intent"]
}`

func TestFlowlinesToolCallMiddleware_Success(t *testing.T) {
	t.Parallel()

	tracerProvider, recorder := newFlowlinesTestTracerProvider()
	email, err := mail.ParseAddr("verified@example.com")
	require.NoError(t, err)
	identity := &coredata.Identity{
		ID:                   gid.New(gid.NewTenantID(), coredata.IdentityEntityType),
		EmailAddress:         email,
		FullName:             "Verified User",
		EmailAddressVerified: true,
	}
	ctx := authn.ContextWithIdentity(t.Context(), identity)
	req := flowlinesTestRequest(
		mcp.Meta{
			"authorization": "Bearer metadata-secret",
			"session.id":    "session-42",
			"user.email":    "spoofed@example.com",
			"user.id":       "spoofed-user",
			"user.name":     "Spoofed User",
		},
	)
	req.Extra = &mcp.RequestExtra{
		Header: http.Header{
			"Authorization": []string{"Bearer header-secret"},
			"User-Agent":    []string{"flowlines-test/1.0"},
		},
	}

	activeSpan := false
	next := func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		activeSpan = trace.SpanFromContext(ctx).IsRecording()
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: `{"ok":true}`},
			},
			StructuredContent: map[string]any{"ok": true},
		}, nil
	}
	middleware := FlowlinesToolCallMiddleware(
		tracerProvider,
		"test-mcp",
		flowlinesTestSchemas(),
	)

	for range 2 {
		result, err := middleware(next)(ctx, "tools/call", req)
		require.NoError(t, err)
		require.NotNil(t, result)
	}

	assert.True(t, activeSpan)
	spans := recorder.Ended()
	require.Len(t, spans, 2)

	first := spanAttributes(spans[0])
	second := spanAttributes(spans[1])
	assert.Equal(t, "execute_tool", first["gen_ai.operation.name"])
	assert.Equal(t, "testTool", first["gen_ai.tool.name"])
	assert.Equal(t, "Need the test result", first["gen_ai.tool.call.reason"])
	assert.Equal(t, "Complete the observability test", first["session.user_intent"])
	assert.Equal(
		t,
		`{"priority":"MEDIUM","reason":"Need the test result","user_intent":"Complete the observability test"}`,
		first["gen_ai.tool.call.arguments"],
	)
	assert.Contains(t, first["gen_ai.tool.call.result"], `"ok":true`)
	assert.Equal(t, "tools/call", first["mcp.method.name"])
	assert.Equal(t, "test-mcp", first["mcp.server.name"])
	assert.Equal(t, "session-42", first["session.id"])
	assert.Equal(t, identity.ID.String(), first["user.id"])
	assert.Equal(t, "Verified User", first["user.name"])
	assert.Equal(t, "verified@example.com", first["user.email"])
	assert.Equal(t, "flowlines-test/1.0", first["user_agent.original"])
	assert.NotEqual(t, first["gen_ai.tool.call.id"], second["gen_ai.tool.call.id"])
	assert.NotContains(t, first, "mcp.request.id")
	assert.NotContains(t, first["gen_ai.tool.call.arguments"], "spoofed")
	assert.NotContains(t, first["gen_ai.tool.call.arguments"], "metadata-secret")
	assert.NotContains(t, first["gen_ai.tool.call.result"], "header-secret")
	assert.Equal(t, codes.Ok, spans[0].Status().Code)
	assert.Equal(t, trace.SpanKindServer, spans[0].SpanKind())
}

func TestFlowlinesToolCallMiddleware_ClientMetadataIdentity(t *testing.T) {
	t.Parallel()

	tracerProvider, recorder := newFlowlinesTestTracerProvider()
	req := flowlinesTestRequest(
		mcp.Meta{
			"session.id": "session-42",
			"user.email": "client@example.com",
			"user.id":    "client-user-17",
			"user.name":  "Client User",
		},
	)
	middleware := FlowlinesToolCallMiddleware(
		tracerProvider,
		"test-mcp",
		flowlinesTestSchemas(),
	)

	_, err := middleware(flowlinesSuccessHandler)(t.Context(), "tools/call", req)
	require.NoError(t, err)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attributes := spanAttributes(spans[0])
	assert.Equal(t, "client-user-17", attributes["user.id"])
	assert.Equal(t, "Client User", attributes["user.name"])
	assert.Equal(t, "client@example.com", attributes["user.email"])
}

func TestFlowlinesToolCallMiddleware_SafeFailure(t *testing.T) {
	t.Parallel()

	tracerProvider, recorder := newFlowlinesTestTracerProvider()
	req := flowlinesTestRequest(mcp.Meta{"user.id": "client-user-17"})
	middleware := FlowlinesToolCallMiddleware(
		tracerProvider,
		"test-mcp",
		flowlinesTestSchemas(),
	)
	const secretSentinel = "backend-secret-sentinel"
	next := func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		_ = secretSentinel
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "internal server error"},
			},
			IsError: true,
		}, nil
	}

	_, err := middleware(next)(t.Context(), "tools/call", req)
	require.NoError(t, err)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attributes := spanAttributes(spans[0])
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Equal(t, "mcp.tool_error", attributes["error.type"])
	assert.Contains(t, attributes["gen_ai.tool.call.result"], "internal server error")
	for _, value := range attributes {
		assert.NotContains(t, fmt.Sprint(value), secretSentinel)
	}
	assert.Empty(t, spans[0].Events())
}

func TestFlowlinesToolCallMiddleware_ProtocolErrorIsSanitized(t *testing.T) {
	t.Parallel()

	tracerProvider, recorder := newFlowlinesTestTracerProvider()
	req := flowlinesTestRequest(mcp.Meta{"user.id": "client-user-17"})
	middleware := FlowlinesToolCallMiddleware(
		tracerProvider,
		"test-mcp",
		flowlinesTestSchemas(),
	)
	protocolErr := fmt.Errorf("backend-secret-sentinel")
	next := func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		return nil, protocolErr
	}

	result, err := middleware(next)(t.Context(), "tools/call", req)
	require.ErrorIs(t, err, protocolErr)
	assert.Nil(t, result)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attributes := spanAttributes(spans[0])
	assert.Equal(t, codes.Error, spans[0].Status().Code)
	assert.Equal(t, "mcp.protocol_error", attributes["error.type"])
	for _, value := range attributes {
		assert.NotContains(t, fmt.Sprint(value), protocolErr.Error())
	}
	assert.Empty(t, spans[0].Events())
}

func TestFlowlinesToolCallMiddleware_ResultSerializationFailureIsFailOpen(t *testing.T) {
	t.Parallel()

	tracerProvider, recorder := newFlowlinesTestTracerProvider()
	req := flowlinesTestRequest(mcp.Meta{"user.id": "client-user-17"})
	middleware := FlowlinesToolCallMiddleware(
		tracerProvider,
		"test-mcp",
		flowlinesTestSchemas(),
	)
	expected := &mcp.CallToolResult{
		StructuredContent: make(chan int),
	}
	next := func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		return expected, nil
	}

	result, err := middleware(next)(t.Context(), "tools/call", req)
	require.NoError(t, err)
	assert.Same(t, expected, result)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	attributes := spanAttributes(spans[0])
	assert.NotContains(t, attributes, "gen_ai.tool.call.result")
	assert.Equal(t, codes.Ok, spans[0].Status().Code)
}

func TestFlowlinesToolCallMiddleware_InvalidInputIsNotEmitted(t *testing.T) {
	t.Parallel()

	tracerProvider, recorder := newFlowlinesTestTracerProvider()
	req := flowlinesTestRequest(mcp.Meta{"user.id": "client-user-17"})
	req.Params.Arguments = json.RawMessage(`{"user_intent":"Complete the observability test"}`)
	middleware := FlowlinesToolCallMiddleware(
		tracerProvider,
		"test-mcp",
		flowlinesTestSchemas(),
	)

	_, err := middleware(flowlinesSuccessHandler)(t.Context(), "tools/call", req)
	require.NoError(t, err)

	assert.Empty(t, recorder.Ended())
}

func newFlowlinesTestTracerProvider() (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	return tracerProvider, recorder
}

func flowlinesTestSchemas() map[string]*jsonschema.Schema {
	return map[string]*jsonschema.Schema{
		"testTool": mcpgenmcp.MustUnmarshalSchema(flowlinesTestSchema),
	}
}

func flowlinesTestRequest(meta mcp.Meta) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Meta: meta,
			Name: "testTool",
			Arguments: json.RawMessage(
				`{"reason":"Need the test result","user_intent":"Complete the observability test"}`,
			),
		},
	}
}

func flowlinesSuccessHandler(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: `{"ok":true}`},
		},
	}, nil
}

func spanAttributes(span sdktrace.ReadOnlySpan) map[string]any {
	attributes := make(map[string]any, len(span.Attributes()))
	for _, attr := range span.Attributes() {
		attributes[string(attr.Key)] = attr.Value.AsInterface()
	}
	return attributes
}
