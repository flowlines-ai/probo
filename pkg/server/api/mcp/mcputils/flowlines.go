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

package mcputils

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.gearno.de/crypto/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	FlowlinesMCPServerName = "probo"
	FlowlinesTracerName    = "go.probo.inc/probo/pkg/server/api/mcp"

	FlowlinesServerInstructions = "Include a non-empty reason explaining the specific call and a stable user_intent describing the end user's goal in every tool call. REQUIRED: call report_outcome exactly once as the last tool call before the final answer in every conversation, including after read-only, partial, failed, or blocked work."

	flowlinesMaxPayloadBytes  = 50_000
	flowlinesMaxReasonRunes   = 128
	flowlinesMaxIntentRunes   = 256
	flowlinesMaxIdentityRunes = 500
)

// FlowlinesMiddleware records complete MCP tool calls using the canonical
// Flowlines GenAI/MCP OpenTelemetry attributes. It deliberately reuses the
// application's tracer provider and leaves exporting to the existing OTLP
// pipeline.
func FlowlinesMiddleware(
	tracer trace.Tracer,
	authenticatedUserID func(context.Context) string,
) func(mcp.MethodHandler) mcp.MethodHandler {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			callReq, ok := req.(*mcp.CallToolRequest)
			if !ok || method != "tools/call" || callReq.Params == nil || tracer == nil {
				return next(ctx, method, req)
			}

			arguments, reason, userIntent, ok := flowlinesArguments(callReq.Params.Arguments)
			if !ok {
				// The generated tool schema returns the client-visible validation
				// error. Calls without valid Flowlines fields are not exported.
				return next(ctx, method, req)
			}

			argumentsJSON, ok := marshalFlowlinesPayload(arguments)
			if !ok {
				return next(ctx, method, req)
			}

			callID, err := uuid.NewV4()
			if err != nil {
				return next(ctx, method, req)
			}

			ctx = flowlinesParentContext(ctx, callReq)
			attrs := []attribute.KeyValue{
				attribute.String("gen_ai.operation.name", "execute_tool"),
				attribute.String("gen_ai.tool.name", callReq.Params.Name),
				attribute.String("gen_ai.tool.call.reason", reason),
				attribute.String("session.user_intent", userIntent),
				attribute.String("gen_ai.tool.call.arguments", argumentsJSON),
				attribute.String("mcp.method.name", "tools/call"),
				attribute.String("mcp.server.name", FlowlinesMCPServerName),
				attribute.String("gen_ai.tool.call.id", callID.String()),
			}

			if sessionID := flowlinesMetadataString(callReq.Params.Meta, "session.id"); sessionID != "" {
				attrs = append(attrs, attribute.String("session.id", sessionID))
			}

			userID := ""
			if authenticatedUserID != nil {
				userID = flowlinesBoundedString(authenticatedUserID(ctx), flowlinesMaxIdentityRunes)
			}
			if userID == "" {
				userID = flowlinesMetadataString(callReq.Params.Meta, "user.id")
			}
			if userID != "" {
				attrs = append(attrs, attribute.String("user.id", userID))
			}

			if client := callReq.ClientInfo(); client != nil {
				if name := flowlinesBoundedString(client.Name, flowlinesMaxIdentityRunes); name != "" {
					attrs = append(attrs, attribute.String("mcp.client.name", name))
				}
				if version := flowlinesBoundedString(client.Version, flowlinesMaxIdentityRunes); version != "" {
					attrs = append(attrs, attribute.String("mcp.client.version", version))
				}
			}

			if version := flowlinesBoundedString(callReq.ProtocolVersion(), flowlinesMaxIdentityRunes); version != "" {
				attrs = append(attrs, attribute.String("mcp.protocol.version", version))
			}

			if extra := callReq.GetExtra(); extra != nil {
				if userAgent := flowlinesBoundedString(extra.Header.Get("User-Agent"), flowlinesMaxIdentityRunes); userAgent != "" {
					attrs = append(attrs, attribute.String("user_agent.original", userAgent))
				}
			}

			ctx, span := tracer.Start(
				ctx,
				"execute_tool "+callReq.Params.Name,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(attrs...),
			)
			defer span.End()

			result, err := next(ctx, method, req)
			failed := err != nil
			errorType := "mcp_protocol_error"

			if callResult, ok := result.(*mcp.CallToolResult); ok {
				if resultJSON, ok := marshalFlowlinesPayload(callResult); ok {
					span.SetAttributes(attribute.String("gen_ai.tool.call.result", resultJSON))
				}
				if callResult.IsError {
					failed = true
					errorType = "mcp_tool_error"
				}
			}

			if failed {
				span.SetStatus(codes.Error, "")
				span.SetAttributes(attribute.String("error.type", errorType))
			} else {
				span.SetStatus(codes.Ok, "")
			}

			return result, err
		}
	}
}

// FlowlinesInstructionsMiddleware publishes the final-call requirement in
// both legacy initialize and new server/discover responses. It also restores
// the explicit safe annotation values that mcpgen omits for false-valued hints.
func FlowlinesInstructionsMiddleware() func(mcp.MethodHandler) mcp.MethodHandler {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil {
				return result, err
			}

			switch result := result.(type) {
			case *mcp.InitializeResult:
				result.Instructions = FlowlinesServerInstructions
			case *mcp.DiscoverResult:
				result.Instructions = FlowlinesServerInstructions
			case *mcp.ListToolsResult:
				for _, tool := range result.Tools {
					if tool.Name != "report_outcome" {
						continue
					}
					if tool.Annotations == nil {
						tool.Annotations = &mcp.ToolAnnotations{}
					}
					tool.Annotations.ReadOnlyHint = true
					tool.Annotations.DestructiveHint = new(false)
					tool.Annotations.IdempotentHint = true
					tool.Annotations.OpenWorldHint = new(false)
				}
			}

			return result, nil
		}
	}
}

func flowlinesArguments(raw json.RawMessage) (map[string]any, string, string, bool) {
	arguments := make(map[string]any)
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, "", "", false
	}

	reason, reasonOK := arguments["reason"].(string)
	userIntent, intentOK := arguments["user_intent"].(string)
	reason = strings.TrimSpace(reason)
	userIntent = strings.TrimSpace(userIntent)

	if !reasonOK || !intentOK || reason == "" || userIntent == "" {
		return nil, "", "", false
	}
	if len([]rune(reason)) > flowlinesMaxReasonRunes || len([]rune(userIntent)) > flowlinesMaxIntentRunes {
		return nil, "", "", false
	}

	return arguments, reason, userIntent, true
}

func marshalFlowlinesPayload(value any) (string, bool) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	if len(payload) <= flowlinesMaxPayloadBytes {
		return string(payload), true
	}

	truncated, err := json.Marshal(
		map[string]any{
			"truncated":     true,
			"original_size": len(payload),
		},
	)
	if err != nil {
		return "", false
	}

	return string(truncated), true
}

func flowlinesMetadataString(meta map[string]any, key string) string {
	value, ok := meta[key].(string)
	if !ok {
		return ""
	}

	return flowlinesBoundedString(value, flowlinesMaxIdentityRunes)
}

func flowlinesBoundedString(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}

	return value
}

func flowlinesParentContext(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}

	extra := req.GetExtra()
	if extra == nil || extra.Header == nil {
		return ctx
	}

	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(extra.Header))
}
