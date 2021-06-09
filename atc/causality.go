package atc

// type CausalityBuild struct {
// 	ID      int         `json:"ID"`
// 	Name    string      `json:"name"`
// 	JobID   int         `json:"job_ID"`
// 	JobName string      `json:"job_name"`
// 	Status  BuildStatus `json:"status"`

// 	ResourceVersions []*CausalityResourceVersion `json:"resource_versions,omitempty"`
// }

// type CausalityResourceVersion struct {
// 	ResourceID        int     `json:"resource_ID"`
// 	ResourceVersionID int     `json:"resource_version_ID"`
// 	ResourceName      string  `json:"resource_name"`
// 	Version           Version `json:"version"`

// 	Builds []*CausalityBuild `json:"builds,omitempty"`
// }

type CausalityJob struct {
	ID   int    `json:"id"`
	Name string `json:"name"`

	BuildIDs []int `json:"build_ids,omitempty"`
}

type CausalityBuild struct {
	ID     int         `json:"id"`
	Name   string      `json:"name"`
	JobId  int         `json:"job_id"`
	Status BuildStatus `json:"status"`

	ResourceVersionIDs []int `json:"resource_version_ids,omitempty"`
}

type CausalityResource struct {
	ID   int    `json:"id"`
	Name string `json:"name"`

	VersionIDs []int `json:"version_ids,omitempty"`
}

type CausalityResourceVersion struct {
	ID      int     `json:"id"`
	Version Version `json:"version"`

	ResourceID int   `json:"resource_id"`
	BuildIDs   []int `json:"build_ids,omitempty"`
}

type Causality struct {
	Jobs             []CausalityJob             `json:"jobs"`
	Builds           []CausalityBuild           `json:"builds"`
	Resources        []CausalityResource        `json:"resources"`
	ResourceVersions []CausalityResourceVersion `json:"resource_versions"`
}
