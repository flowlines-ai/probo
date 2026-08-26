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

package mcp_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.probo.inc/probo/e2e/internal/testutil"
)

const (
	flowlinesAcceptanceReason = "Verify that Flowlines staging receives this harmless MCP observability call"
	flowlinesAcceptanceIntent = "Validate Probo MCP observability in the Flowlines staging dashboard."
)

func TestMCP_FlowlinesObservabilityAcceptance(t *testing.T) {
	t.Parallel()

	if os.Getenv("PROBO_E2E_FLOWLINES_ACCEPTANCE") == "" {
		t.Skip("set PROBO_E2E_FLOWLINES_ACCEPTANCE to run the live Flowlines staging acceptance test")
	}

	owner := testutil.NewClient(t, testutil.RoleOwner)
	mc := testutil.NewMCPClient(t, owner)
	t.Logf("Flowlines acceptance session.id=%s", mc.AnalyticsSessionID())

	for range 10 {
		var result struct {
			Organizations []struct {
				ID string `json:"id"`
			} `json:"organizations"`
		}
		mc.CallToolInto("listOrganizations", map[string]any{
			"reason":      flowlinesAcceptanceReason,
			"user_intent": flowlinesAcceptanceIntent,
		}, &result)

		assert.NotEmpty(t, result.Organizations)
	}

	var outcome struct {
		Recorded bool `json:"recorded"`
	}
	mc.CallToolInto("report_outcome", map[string]any{
		"reason":          "Record the final result of the Flowlines staging observability acceptance run",
		"user_intent":     flowlinesAcceptanceIntent,
		"status":          "accomplished",
		"outcome_summary": "Ten harmless listOrganizations calls completed successfully in one observed MCP session.",
	}, &outcome)

	assert.True(t, outcome.Recorded)
}
