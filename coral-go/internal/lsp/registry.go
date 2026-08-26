package lsp

import (
	"path/filepath"
	"strings"
)

type ServerDefinition struct {
	ID                 string
	Languages          []string
	Extensions         []string
	Command            string
	Args               []string
	RootMarkers        []string
	InitializationOpts func(workspace string) any
}

type Registry struct{ definitions []ServerDefinition }

func DefaultRegistry() *Registry {
	return NewRegistry([]ServerDefinition{{
		ID: "gopls", Languages: []string{"go"}, Extensions: []string{".go"},
		Command: "gopls", RootMarkers: []string{"go.work", "go.mod"},
	}})
}

func NewRegistry(definitions []ServerDefinition) *Registry {
	return &Registry{definitions: append([]ServerDefinition(nil), definitions...)}
}

func (r *Registry) ForFile(path string) (ServerDefinition, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	for _, d := range r.definitions {
		for _, candidate := range d.Extensions {
			if strings.EqualFold(candidate, ext) {
				return d, true
			}
		}
	}
	return ServerDefinition{}, false
}

func (d ServerDefinition) Language() string {
	if len(d.Languages) > 0 {
		return d.Languages[0]
	}
	return ""
}
