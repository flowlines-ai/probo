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
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerInstructionsMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result mcp.Result
		got    func(mcp.Result) string
	}{
		{
			name:   "initialize",
			result: &mcp.InitializeResult{Instructions: "Existing instructions."},
			got: func(result mcp.Result) string {
				return result.(*mcp.InitializeResult).Instructions
			},
		},
		{
			name:   "discover",
			result: &mcp.DiscoverResult{Instructions: "Existing instructions."},
			got: func(result mcp.Result) string {
				return result.(*mcp.DiscoverResult).Instructions
			},
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				t.Parallel()

				next := func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					return tt.result, nil
				}
				middleware := ServerInstructionsMiddleware("Call report_outcome last.")

				result, err := middleware(next)(t.Context(), tt.name, nil)
				require.NoError(t, err)
				assert.Equal(
					t,
					"Existing instructions.\n\nCall report_outcome last.",
					tt.got(result),
				)
			},
		)
	}
}
