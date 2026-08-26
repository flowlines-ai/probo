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

package mcp_v1

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.probo.inc/probo/pkg/server/api/mcp/v1/types"
	"sigs.k8s.io/yaml"
)

type flowlinesSpecification struct {
	Tools []struct {
		Description string `json:"description"`
		InputSchema struct {
			Ref string `json:"$ref"`
		} `json:"inputSchema"`
		Name string `json:"name"`
	} `json:"tools"`
}

func TestFlowlinesToolSchemas(t *testing.T) {
	t.Parallel()

	specification := loadFlowlinesSpecification(t)
	require.Len(t, types.FlowlinesToolInputSchemas, len(specification.Tools))

	for _, tool := range specification.Tools {
		tool := tool
		t.Run(
			tool.Name,
			func(t *testing.T) {
				t.Parallel()

				schema, ok := types.FlowlinesToolInputSchemas[tool.Name]
				require.True(t, ok)
				encoded, err := json.Marshal(schema)
				require.NoError(t, err)

				var document struct {
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				}
				require.NoError(t, json.Unmarshal(encoded, &document))
				assert.Contains(t, document.Properties, "reason")
				assert.Contains(t, document.Properties, "user_intent")
				assert.Contains(t, document.Required, "reason")
				assert.Contains(t, document.Required, "user_intent")
			},
		)
	}
}

func TestReportOutcomeContract(t *testing.T) {
	t.Parallel()

	specification := loadFlowlinesSpecification(t)
	var description string
	for _, tool := range specification.Tools {
		if tool.Name == "report_outcome" {
			description = tool.Description
			break
		}
	}

	assert.True(t, strings.HasPrefix(description, "REQUIRED final call in every conversation"))
	assert.Contains(t, serverInstructions, "report_outcome exactly once as the final tool call")
	assert.Contains(t, serverInstructions, "reason")
	assert.Contains(t, serverInstructions, "user_intent")

	_, output, err := (&Resolver{}).ReportOutcomeTool(t.Context(), nil, &types.ReportOutcomeInput{})
	require.NoError(t, err)
	assert.True(t, output.Recorded)
}

func loadFlowlinesSpecification(t *testing.T) flowlinesSpecification {
	t.Helper()

	data, err := os.ReadFile("specification.yaml")
	require.NoError(t, err)

	var specification flowlinesSpecification
	require.NoError(t, yaml.Unmarshal(data, &specification))
	return specification
}
