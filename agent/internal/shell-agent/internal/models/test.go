package models

import (
	"context"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// DeterministicModel returns pre-seeded responses, useful for testing.
type DeterministicModel struct {
	config    *minisweagent.ModelConfig
	responses []minisweagent.QueryResult
	idx       int
}

// NewDeterministicModel builds a deterministic model with the provided responses.
func NewDeterministicModel(responses []minisweagent.QueryResult) *DeterministicModel {
	return &DeterministicModel{
		config:    &minisweagent.ModelConfig{ModelName: "deterministic"},
		responses: responses,
	}
}

func (m *DeterministicModel) Config() interface{} { return m.config }
func (m *DeterministicModel) Cost() float64       { return 0 }
func (m *DeterministicModel) NCalls() int         { return m.idx }
func (m *DeterministicModel) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{
		"model_name":    m.config.ModelName,
		"ModelName":     m.config.ModelName,
		"n_model_calls": m.idx,
		"NModelCalls":   m.idx,
		"model_cost":    0.0,
		"ModelCost":     0.0,
	}
}

// Query returns the next canned response, wrapping when exhausted.
func (m *DeterministicModel) Query(ctx context.Context, msgs []minisweagent.Message, opts ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	if len(m.responses) == 0 {
		return minisweagent.QueryResult{}, nil
	}
	resp := m.responses[m.idx%len(m.responses)]
	m.idx++
	return resp, nil
}
