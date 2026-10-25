package headless

import (
	"encoding/json"
	"os"
	"strings"
)

// This file carries adhd's registry identity: the project route it belongs to
// and the projects it depends on. A dependent resolves a project by route, not
// by the endpoint it happens to be on.
//
// The config value wins over the seed file (a deployment may want a stable
// route without carrying a seed), and LEZZ_PROJECT_ROUTE is the environment
// override between them.

// projectRoute returns this project's routing id.
func (s *Server) projectRoute() string {
	if s.cfg == nil {
		return ""
	}
	if route := strings.TrimSpace(s.cfg.Project.Route); route != "" {
		return route
	}
	if route := strings.TrimSpace(os.Getenv("LEZZ_PROJECT_ROUTE")); route != "" {
		return route
	}
	return seedRouteFromFile("seed.json")
}

// seedRouteFromFile reads the route out of a lezz seed file. The file is a
// stable wire format and this client needs one field from it, so it is read
// directly rather than importing lezz's internal package.
//
// `project.route` is the field (lezz ADR 0003); `seed.id` is read as the legacy
// alias for one release, and only when the route is absent.
func seedRouteFromFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var seedFile struct {
		Seed struct {
			ID string `json:"id"`
		} `json:"seed"`
		Project struct {
			Route string `json:"route"`
		} `json:"project"`
	}
	if err := json.Unmarshal(b, &seedFile); err != nil {
		return ""
	}
	if route := strings.TrimSpace(seedFile.Project.Route); route != "" {
		return route
	}
	return strings.TrimSpace(seedFile.Seed.ID)
}

// isotopeDependency references a project this instance depends on: the route
// says where that project is, the contract pins the signed spec it implements.
type isotopeDependency struct {
	ProjectRoute string `json:"project_route"`
	Contract     string `json:"contract,omitempty"`
}

// isotopeReferences is the identity block sent with a registration. None of it
// is a trust claim; the registry's assigned rung stays the judgement.
type isotopeReferences struct {
	ProjectRoute string              `json:"project_route,omitempty"`
	Contract     string              `json:"contract,omitempty"`
	DependsOn    []isotopeDependency `json:"depends_on,omitempty"`
}

// isotopeReferences collects this instance's route and the projects it depends
// on. A smoke-alarm endpoint without a project route is a watcher, not a
// resolvable dependency, so it is not published as one.
func (s *Server) isotopeReferences() isotopeReferences {
	if s.cfg == nil {
		return isotopeReferences{}
	}

	refs := isotopeReferences{
		ProjectRoute: s.projectRoute(),
		Contract:     strings.TrimSpace(s.cfg.Project.Contract),
	}
	for _, ep := range s.cfg.SmokeAlarm {
		route := strings.TrimSpace(ep.ProjectRoute)
		if route == "" {
			continue
		}
		refs.DependsOn = append(refs.DependsOn, isotopeDependency{
			ProjectRoute: route,
			Contract:     strings.TrimSpace(ep.Contract),
		})
	}
	return refs
}
