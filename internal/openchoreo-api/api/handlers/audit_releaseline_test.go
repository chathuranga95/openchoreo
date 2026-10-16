// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	openchoreov1alpha1 "github.com/openchoreo/openchoreo/api/v1alpha1"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/api/gen"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/models"
	"github.com/openchoreo/openchoreo/internal/openchoreo-api/services/handlerservices"
	workflowrunmocks "github.com/openchoreo/openchoreo/internal/openchoreo-api/services/workflowrun/mocks"
	"github.com/openchoreo/openchoreo/internal/server/middleware/audit"
)

func TestCreateComponentWorkflowRun_AuditsReleaseLine(t *testing.T) {
	for name, tc := range map[string]struct {
		releaseLine  string
		wantMetadata map[string]any
	}{
		"release line":    {releaseLine: "next", wantMetadata: map[string]any{"releaseLine": "next"}},
		"no release line": {},
	} {
		t.Run(name, func(t *testing.T) {
			svc := workflowrunmocks.NewMockService(t)
			svc.EXPECT().TriggerWorkflow(mock.Anything, "ns", "", "hello", "", tc.releaseLine).
				Return(&models.WorkflowRunTriggerResponse{Name: "hello-run-1"}, nil)
			svc.EXPECT().GetWorkflowRun(mock.Anything, "ns", "hello-run-1").
				Return(&openchoreov1alpha1.WorkflowRun{ObjectMeta: metav1.ObjectMeta{Name: "hello-run-1", UID: "uid-1"}}, nil)
			h := &Handler{
				services: &handlerservices.Services{WorkflowRunService: svc},
				logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			}

			body := &gen.CreateComponentWorkflowRunJSONRequestBody{}
			if tc.releaseLine != "" {
				body.ReleaseLine = &tc.releaseLine
			}
			ctx, data := audit.NewAuditContext(context.Background(), nil, audit.RequestInfo{})
			_, err := h.CreateComponentWorkflowRun(ctx, gen.CreateComponentWorkflowRunRequestObject{
				NamespaceName: "ns",
				ComponentName: "hello",
				Body:          body,
			})
			require.NoError(t, err)
			require.NotNil(t, data.Resource)
			assert.Equal(t, "hello-run-1", data.Resource.Name)
			assert.Equal(t, tc.wantMetadata, data.Resource.Metadata)
		})
	}
}

func TestExecHandler_AuditsReleaseLine(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	mw, err := NewExecWirelogsAuditMiddleware(logger, newTestAuditEmitter(t, &buf), audit.MiddlewareConfig{Enabled: true})
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle(ExecRoutePattern, mw.Handler(NewExecHandler(nil, nil, "", nil, nil, logger)))
	mux.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/exec/namespaces/ns-a/components/comp-a?releaseLine=next", nil))

	records := auditRecords(t, &buf)
	require.Len(t, records, 1, "expected exactly one AUDIT-LOG record, got:\n%s", buf.String())
	resource, ok := records[0]["resource"].(map[string]any)
	require.True(t, ok, "resource not populated: %+v", records[0])
	assert.Equal(t, map[string]any{"releaseLine": "next"}, resource["metadata"])
}
