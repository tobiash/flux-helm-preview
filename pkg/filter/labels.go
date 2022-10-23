package filter

import (
	"sigs.k8s.io/kustomize/kyaml/kio"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

var _ kio.Filter = &LabelRemover{}

type LabelRemover struct {
	Kind   string   `yaml:"kind,omitempty"`
	Labels []string `yaml:"labels"`
}

func (lr LabelRemover) Filter(input []*yaml.RNode) ([]*yaml.RNode, error) {
	for i := range input {
		node := input[i]
		for _, l := range lr.Labels {
		  _, err := node.Pipe(
				&yaml.PathGetter{Path: []string{ "metadata", "labels" }},
				&yaml.FieldClearer{Name: l},
			)
			if err != nil {
				return nil, err
			}
		}
	}
	return input, nil
}
