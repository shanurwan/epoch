package engine

import (
	"context"
	"fmt"

	"github.com/shanurwan/epoch/internal/artifact"
	"github.com/shanurwan/epoch/internal/config"
	"github.com/shanurwan/epoch/internal/scenario"
)

type Plan struct {
	Scenario *scenario.Scenario
	Input    []byte
	Image    *artifact.Prepared
}

func Validate(ctx context.Context, c config.Config, path string) (*Plan, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	b, err := config.ReadBounded(path, scenario.MaxFileSize)
	if err != nil {
		return nil, err
	}
	s, err := scenario.Decode(b)
	if err != nil {
		return nil, err
	}
	if s.Resources.VCPUCount > c.Policy.MaxVCPUs || s.Resources.MemoryMiB > c.Policy.MaxMemoryMiB || s.TimeoutMS > c.Policy.MaxTimeoutMS {
		return nil, fmt.Errorf("scenario exceeds operator policy")
	}
	a, err := artifact.Load(ctx, c.ArtifactDir, s.Image, c.Policy.MaxDiskBytes)
	if err != nil {
		return nil, err
	}
	if s.Workload != a.Workload.ID {
		return nil, fmt.Errorf("scenario workload differs from prepared image")
	}
	for _, step := range s.Steps {
		switch step.Op {
		case "action.exec":
			if _, ok := a.Workload.Actions[step.Action]; !ok {
				return nil, fmt.Errorf("step %s: undeclared action %s", step.ID, step.Action)
			}
		case "service.start", "service.stop":
			if _, ok := a.Workload.Services[step.Service]; !ok {
				return nil, fmt.Errorf("step %s: undeclared service %s", step.ID, step.Service)
			}
		}
	}
	return &Plan{Scenario: s, Input: b, Image: a}, nil
}
