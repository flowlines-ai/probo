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
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.probo.inc/probo/pkg/gid"
	"go.probo.inc/probo/pkg/server/api/authn"
)

const (
	flowlinesTracerName       = "go.probo.inc/probo/pkg/server/api/mcp"
	flowlinesMaxPayloadLength = 50_000
	flowlinesMaxMetadataValue = 500
)

type flowlinesUser struct {
	id    string
	name  string
	email string
}

// FlowlinesToolCallMiddleware emits one canonical MCP server span around each
// schema-valid tool execution. Telemetry failures are deliberately fail-open.
func FlowlinesToolCallMiddleware(
	tracerProvider trace.TracerProvider,
	serverName string,
	schemas map[string]*jsonschema.Schema,
) func(mcp.MethodHandler) mcp.MethodHandler {
	if tracerProvider == nil {
		tracerProvider = otel.GetTracerProvider()
	}

	tracer := tracerProvider.Tracer(flowlinesTracerName)
	resolvedSchemas := resolveSchemas(schemas)

	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			call, ok := req.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok || call.Params == nil {
				return next(ctx, method, req)
			}

			arguments, argumentsJSON, ok := validatedArguments(
				resolvedSchemas[call.Params.Name],
				call.Params.Arguments,
			)
			if !ok {
				return next(ctx, method, req)
			}

			reason, reasonOK := nonEmptyString(arguments["reason"])
			userIntent, userIntentOK := nonEmptyString(arguments["user_intent"])
			user, userOK := flowlinesUserFromRequest(ctx, call)
			if !reasonOK || !userIntentOK || !userOK {
				return next(ctx, method, req)
			}

			attributes := []attribute.KeyValue{
				attribute.String("gen_ai.operation.name", "execute_tool"),
				attribute.String("gen_ai.tool.name", call.Params.Name),
				attribute.String("gen_ai.tool.call.reason", reason),
				attribute.String("session.user_intent", userIntent),
				attribute.String("gen_ai.tool.call.arguments", argumentsJSON),
				attribute.String("mcp.method.name", "tools/call"),
				attribute.String("mcp.server.name", serverName),
				attribute.String("gen_ai.tool.call.id", uuid.NewString()),
				attribute.String("user.id", user.id),
			}
			attributes = appendRequestAttributes(attributes, call, user)

			spanCtx, span := tracer.Start(
				ctx,
				fmt.Sprintf("execute_tool %s", call.Params.Name),
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(attributes...),
			)
			defer span.End()

			result, err := next(spanCtx, method, req)
			if err != nil {
				span.SetStatus(codes.Error, "")
				span.SetAttributes(attribute.String("error.type", "mcp.protocol_error"))
				return result, err
			}

			callResult, ok := result.(*mcp.CallToolResult)
			if !ok || callResult == nil {
				span.SetStatus(codes.Error, "")
				span.SetAttributes(attribute.String("error.type", "mcp.empty_result"))
				return result, nil
			}

			if resultJSON, ok := boundedJSON(callResult); ok {
				span.SetAttributes(attribute.String("gen_ai.tool.call.result", resultJSON))
			}

			if callResult.IsError {
				span.SetStatus(codes.Error, "")
				span.SetAttributes(attribute.String("error.type", "mcp.tool_error"))
			} else {
				span.SetStatus(codes.Ok, "")
			}

			return result, nil
		}
	}
}

func resolveSchemas(schemas map[string]*jsonschema.Schema) map[string]*jsonschema.Resolved {
	resolvedSchemas := make(map[string]*jsonschema.Resolved, len(schemas))
	for name, schema := range schemas {
		if schema == nil {
			continue
		}

		resolved, err := schema.Resolve(nil)
		if err == nil {
			resolvedSchemas[name] = resolved
		}
	}

	return resolvedSchemas
}

func validatedArguments(
	schema *jsonschema.Resolved,
	raw json.RawMessage,
) (arguments map[string]any, argumentsJSON string, ok bool) {
	if schema == nil {
		return nil, "", false
	}

	defer func() {
		if recover() != nil {
			arguments = nil
			argumentsJSON = ""
			ok = false
		}
	}()

	arguments = make(map[string]any)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return nil, "", false
		}
	}

	if err := schema.ApplyDefaults(&arguments); err != nil {
		return nil, "", false
	}

	if err := schema.Validate(&arguments); err != nil {
		return nil, "", false
	}

	argumentsJSON, ok = boundedJSON(arguments)
	return arguments, argumentsJSON, ok
}

func flowlinesUserFromRequest(ctx context.Context, req *mcp.CallToolRequest) (flowlinesUser, bool) {
	identity := authn.IdentityFromContext(ctx)
	meta := req.Params.Meta

	if identity == nil || identity.ID == gid.Nil {
		id, ok := metadataString(meta, "user.id")
		if !ok {
			return flowlinesUser{}, false
		}

		name, _ := metadataString(meta, "user.name")
		email, _ := metadataString(meta, "user.email")
		return flowlinesUser{id: id, name: name, email: email}, true
	}

	user := flowlinesUser{
		id:   boundedString(identity.ID.String()),
		name: boundedString(identity.FullName),
	}
	if identity.EmailAddressVerified {
		user.email = boundedString(identity.EmailAddress.String())
	}
	if user.name == "" {
		user.name, _ = metadataString(meta, "user.name")
	}
	if user.email == "" {
		user.email, _ = metadataString(meta, "user.email")
	}

	return user, true
}

func appendRequestAttributes(
	attributes []attribute.KeyValue,
	req *mcp.CallToolRequest,
	user flowlinesUser,
) []attribute.KeyValue {
	if sessionID, ok := metadataString(req.Params.Meta, "session.id"); ok {
		attributes = append(attributes, attribute.String("session.id", sessionID))
	}
	if user.name != "" {
		attributes = append(attributes, attribute.String("user.name", user.name))
	}
	if user.email != "" {
		attributes = append(attributes, attribute.String("user.email", user.email))
	}
	if client := req.ClientInfo(); client != nil {
		if name := boundedString(client.Name); name != "" {
			attributes = append(attributes, attribute.String("mcp.client.name", name))
		}
		if version := boundedString(client.Version); version != "" {
			attributes = append(attributes, attribute.String("mcp.client.version", version))
		}
	}
	if protocolVersion := boundedString(req.ProtocolVersion()); protocolVersion != "" {
		attributes = append(attributes, attribute.String("mcp.protocol.version", protocolVersion))
	}
	if extra := req.GetExtra(); extra != nil && extra.Header != nil {
		if userAgent := boundedString(extra.Header.Get("User-Agent")); userAgent != "" {
			attributes = append(attributes, attribute.String("user_agent.original", userAgent))
		}
	}

	return attributes
}

func metadataString(meta mcp.Meta, key string) (string, bool) {
	value, ok := meta[key].(string)
	if !ok {
		return "", false
	}

	value = boundedString(value)
	return value, value != ""
}

func nonEmptyString(value any) (string, bool) {
	stringValue, ok := value.(string)
	return stringValue, ok && strings.TrimSpace(stringValue) != ""
}

func boundedString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > flowlinesMaxMetadataValue {
		value = value[:flowlinesMaxMetadataValue]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}

	return value
}

func boundedJSON(value any) (encodedJSON string, ok bool) {
	defer func() {
		if recover() != nil {
			encodedJSON = ""
			ok = false
		}
	}()

	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	if len(encoded) <= flowlinesMaxPayloadLength {
		return string(encoded), true
	}

	truncated, err := json.Marshal(
		map[string]any{
			"original_size": len(encoded),
			"truncated":     true,
		},
	)
	if err != nil {
		return "", false
	}

	return string(truncated), true
}
