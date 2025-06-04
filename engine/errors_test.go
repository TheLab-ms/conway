package engine

import (
	"testing"

	snaptest "github.com/TheLab-ms/conway/internal/testing"
)

func TestRenderError(t *testing.T) {
	tests := []struct {
		name        string
		error       *httpError
		fixtureName string
	}{
		{
			name: "client_error_400",
			error: &httpError{
				StatusCode: 400,
				Message:    "Invalid request parameter",
			},
			fixtureName: "_client_error",
		},
		{
			name: "client_error_404",
			error: &httpError{
				StatusCode: 404,
				Message:    "Page not found",
			},
			fixtureName: "_not_found",
		},
		{
			name: "server_error_500",
			error: &httpError{
				StatusCode: 500,
				Message:    "Internal server error",
			},
			fixtureName: "_server_error",
		},
		{
			name: "server_error_502",
			error: &httpError{
				StatusCode: 502,
				Message:    "Bad gateway",
			},
			fixtureName: "_bad_gateway",
		},
		{
			name: "edge_case_499",
			error: &httpError{
				StatusCode: 499,
				Message:    "Client closed request",
			},
			fixtureName: "_edge_case_499",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderError(tt.error)
			snaptest.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}