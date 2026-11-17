// Package catalog embeds the official sample config of every DataKit collector.
package catalog

import (
	_ "embed"
	"encoding/json"
	"sort"
)

//go:embed samples.json
var raw []byte

// Sample is the official sample config of one collector.
type Sample struct {
	ID     string `json:"id"`     // collector directory name, also the logo name
	Input  string `json:"input"`  // name used in [[inputs.<name>]]
	Sample string `json:"sample"` // official conf sample
}

// Samples returns all samples sorted by id.
func Samples() ([]Sample, error) {
	var m map[string]struct {
		Input  string `json:"input"`
		Sample string `json:"sample"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := make([]Sample, 0, len(m))
	for id, s := range m {
		out = append(out, Sample{ID: id, Input: s.Input, Sample: s.Sample})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
