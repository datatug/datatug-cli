package api

import (
	"context"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

// LocalProjectDbServerDiscovery returns active server definitions without the
// owner-only lifecycle inventory. Full catalogs and direct references stay separate.
func LocalProjectDbServerDiscovery(ctx context.Context, ref dto.ProjectRef, branch string) ([]datatug.ProjDbServer, error) {
	check, err := beginProjectDiscovery(ctx, branch)
	if err != nil {
		return nil, err
	}
	rows, err := localProjectDbServers(ctx, ref, branch, true)
	if freshnessErr := check(); freshnessErr != nil {
		err = freshnessErr
	}
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// LocalProjectMeaningDiscovery is a display projection of the complete catalog.
// Its revision still names that complete snapshot; saved-reference resolution
// and mutation validation continue to read the unfiltered definitions. This
// revision is not an active-view cache key. Archive changes affect display only.
func LocalProjectMeaningDiscovery(ctx context.Context, ref dto.ProjectRef, branch string) (datatug.ProjectMeaningsCatalog, error) {
	check, err := beginProjectDiscovery(ctx, branch)
	if err != nil {
		return datatug.ProjectMeaningsCatalog{}, err
	}
	value, err := localProjectMeaningsCatalog(ctx, ref, branch, true)
	if freshnessErr := check(); freshnessErr != nil {
		err = freshnessErr
	}
	if err != nil {
		return datatug.ProjectMeaningsCatalog{}, err
	}
	return value, nil
}

func beginProjectDiscovery(ctx context.Context, branch string) (func() error, error) {
	if ctx == nil || branch == "" {
		return nil, dto.ErrUnsupportedCapability
	}
	generation := SecurityContextID()
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		if !ValidateSecurityContext(generation) {
			return secureread.ErrAccessDenied
		}
		return nil
	}
	if err := check(); err != nil {
		return nil, err
	}
	return check, nil
}

func projectMeaningDiscovery(full datatug.ProjectMeaningsCatalog, archived map[datatug.ProjectObjectRef]bool) datatug.ProjectMeaningsCatalog {
	out := full
	out.Entities = make([]datatug.MeaningEntity, 0, len(full.Entities))
	for _, entity := range full.Entities {
		if archived[datatug.ProjectObjectRef{Kind: "meaning", ID: entity.DocumentID + "/" + entity.ConceptID}] {
			continue
		}

		hiddenQuery := false
		if entity.Origin == "query" {
			for _, resource := range entity.Resources {
				if resource.Kind == "query" && archived[datatug.ProjectObjectRef{Kind: "query", ID: resource.ID}] {
					hiddenQuery = true
					break
				}
			}
		}
		if hiddenQuery {
			continue
		}
		entity.Resources = meaningDiscoveryResources(entity.Resources, archived)
		fields := make([]datatug.MeaningField, 0, len(entity.Fields))
		for _, field := range entity.Fields {
			if !archived[datatug.ProjectObjectRef{Kind: "meaning", ID: field.DocumentID + "/" + field.ConceptID}] {
				field.Resources = meaningDiscoveryResources(field.Resources, archived)
				fields = append(fields, field)
			}
		}
		entity.Fields = fields
		out.Entities = append(out.Entities, entity)
	}
	return out
}

// Archive affects discovery only; retained catalogs still contain every reference.
func meaningDiscoveryResources(resources []datatug.MeaningResource, archived map[datatug.ProjectObjectRef]bool) []datatug.MeaningResource {
	out := make([]datatug.MeaningResource, 0, len(resources))
	for _, resource := range resources {
		if !archived[datatug.ProjectObjectRef{Kind: resource.Kind, ID: resource.ID}] {
			out = append(out, resource)
		}
	}
	return out
}
