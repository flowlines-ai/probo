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

package mcp_v1_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.probo.inc/probo/pkg/server/api/mcp/mcputils"
	mcp_v1 "go.probo.inc/probo/pkg/server/api/mcp/v1"
	"go.probo.inc/probo/pkg/server/api/mcp/v1/server"
)

func TestReportOutcomeTool_Contract(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mcpServer := server.New(new(mcp_v1.Resolver))
	mcpServer.AddReceivingMiddleware(mcputils.FlowlinesInstructionsMiddleware())
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpServer.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	client := mcp.NewClient(
		&mcp.Implementation{Name: "probo-flowlines-test", Version: "1.0.0"},
		nil,
	)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, clientSession.Close())
		require.NoError(t, serverSession.Close())
	})

	listResult, err := clientSession.ListTools(ctx, nil)
	require.NoError(t, err)
	var outcomeTool *mcp.Tool
	for _, tool := range listResult.Tools {
		toolSchemaJSON, marshalErr := json.Marshal(tool.InputSchema)
		require.NoError(t, marshalErr)
		var toolSchema struct {
			Required []string `json:"required"`
		}
		require.NoError(t, json.Unmarshal(toolSchemaJSON, &toolSchema))
		assert.Contains(t, toolSchema.Required, "reason", tool.Name)
		assert.Contains(t, toolSchema.Required, "user_intent", tool.Name)

		if tool.Name == "report_outcome" {
			outcomeTool = tool
		}
	}
	require.NotNil(t, outcomeTool)
	assert.True(t, strings.HasPrefix(outcomeTool.Description, "REQUIRED final call in every conversation"))
	assert.True(t, outcomeTool.Annotations.ReadOnlyHint)
	assert.False(t, *outcomeTool.Annotations.DestructiveHint)
	assert.True(t, outcomeTool.Annotations.IdempotentHint)
	assert.False(t, *outcomeTool.Annotations.OpenWorldHint)

	schemaJSON, err := json.Marshal(outcomeTool.InputSchema)
	require.NoError(t, err)
	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(schemaJSON, &schema))
	assert.ElementsMatch(
		t,
		[]string{"reason", "user_intent", "status", "outcome_summary"},
		schema.Required,
	)
	assert.Contains(t, schema.Properties, "unmet_needs")

	result, err := clientSession.CallTool(
		ctx,
		&mcp.CallToolParams{
			Meta: mcp.Meta{"session.id": "report-session"},
			Name: "report_outcome",
			Arguments: map[string]any{
				"reason":          "Report the completed verification",
				"user_intent":     "Verify the Flowlines MCP integration",
				"status":          "accomplished",
				"outcome_summary": "The integration is complete. Local verification passed.",
			},
		},
	)
	require.NoError(t, err)
	assert.False(t, result.IsError)
	resultJSON, err := json.Marshal(result.StructuredContent)
	require.NoError(t, err)
	assert.JSONEq(t, `{"accepted":true}`, string(resultJSON))

	invalidResult, err := clientSession.CallTool(
		ctx,
		&mcp.CallToolParams{
			Name: "report_outcome",
			Arguments: map[string]any{
				"reason":      "Report an incomplete outcome",
				"user_intent": "Verify the Flowlines MCP integration",
				"status":      "partial",
			},
		},
	)
	require.NoError(t, err)
	assert.True(t, invalidResult.IsError)

	initializeResult := clientSession.InitializeResult()
	require.NotNil(t, initializeResult)
	assert.Contains(t, initializeResult.Instructions, "report_outcome")
	assert.Contains(t, initializeResult.Instructions, "last tool call")
}
