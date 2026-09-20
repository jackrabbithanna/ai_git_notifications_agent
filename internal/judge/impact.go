package judge

// ImpactVersion identifies the PR impact question set; bump when rubric text changes.
const ImpactVersion = "impact.v1"

// Impact returns the impact.v1 question set (PLAN.md §4.4), asked over an ImpactState.
func Impact() Set {
	return Set{Version: ImpactVersion, Questions: []Question{
		{
			ID: "change_kind", Kind: Choice,
			Instructions: "Given `pr` (title, body, labels), `layers_touched`, `signals`, `surface_hits` and the `changed_files` patches, what single kind of change is this pull request primarily?",
			Options: []Option{
				{"bugfix", "Corrects behaviour without changing public surfaces or data shapes"},
				{"refactor_internal", "Restructures internals; public APIs, hooks, schema and UI extension points unchanged"},
				{"new_feature", "Adds new capability or new optional API/UI surface without altering existing ones"},
				{"architectural", "Changes how major components fit together: services, dispatching, lifecycle, extension mechanisms"},
				{"api_change", "Alters an existing public API, hook, function signature, return shape, permission check or setting definition"},
				{"data_model_change", "Alters schema, entity fields, DAO/BAO definitions, migrations or stored data formats"},
				{"deprecation_or_removal", "Deprecates or removes something dependents may use"},
				{"dependency_or_packaging", "Dependency bumps, build, packaging, CI or tooling only"},
				{"docs_tests_only", "Only documentation or tests changed"},
			},
		},
		{
			ID: "affects_downstream", Kind: Noul,
			Instructions: "Would code built on this project as described in `downstream_description` need changes, or at least re-testing, because of this pull request? Weigh `surface_hits` and `layers_touched` heavily; ignore test-only and docs-only files.",
			NoulTrue:     "Yes: dependents that use the touched surfaces would likely need code changes or a re-test",
			NoulFalse:    "No: the change is internal, additive-and-optional, or in areas dependents do not use",
		},
		{
			ID: "downstream_impact", Kind: Score,
			Instructions: "How much would this pull request affect code built on this project as described in `downstream_description`?",
			Levels: []string{
				"None: internal change, tests/docs/packaging only, or areas dependents do not touch",
				"Possible: new optional capability, or a change in a surface dependents might use; worth a glance",
				"Likely: behaviour, defaults or shape changed in a surface dependents commonly use; re-test needed",
				"Certain: signature, schema, hook or removal that will break or require updating dependent code",
			},
		},
		{
			ID: "backward_compatible", Kind: Noul,
			Instructions: "Judging from the patches in `changed_files` and `surface_hits`, do existing callers of the touched surfaces keep working unchanged after this pull request?",
			NoulTrue:     "Yes: existing calls, hooks, schema readers and templates keep working (additions are optional, defaults preserved)",
			NoulFalse:    "No: something existing callers rely on changed shape, name, meaning, default or was removed",
		},
		{
			ID: "needs_my_attention_now", Kind: Noul,
			Instructions: "Given `downstream_description` and `pr.state` (open vs merged), should the user look at this pull request soon — before it lands to comment, or now that it landed to adapt?",
			NoulTrue:     "Yes: it touches what the user depends on and the timing matters (open with a chance to influence, or landed and requires adaptation)",
			NoulFalse:    "No: it can wait or does not concern the user's work",
		},
	}}
}

// ImpactState is the JSON state for impact.v1. Names are referenced by the instructions.
type ImpactState struct {
	PR                    ImpactPR      `json:"pr"`
	LayersTouched         []ImpactLayer `json:"layers_touched"`
	Signals               []string      `json:"signals"`
	SurfaceHits           []ImpactHit   `json:"surface_hits"`
	ChangedFiles          []ImpactFile  `json:"changed_files"`
	DownstreamDescription string        `json:"downstream_description"`
}

type ImpactPR struct {
	Forge      string   `json:"forge"`
	Repo       string   `json:"repo"`
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	Body       string   `json:"body,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Base       string   `json:"base,omitempty"`
	State      string   `json:"state"` // open | merged | closed
	Draft      bool     `json:"draft,omitempty"`
	Author     string   `json:"author,omitempty"`
	FilesTotal int      `json:"files_total"`
	Additions  int      `json:"additions"`
	Deletions  int      `json:"deletions"`
}

type ImpactLayer struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Weight    float64  `json:"weight"`
	Files     []string `json:"files"`
	Additions int      `json:"additions"`
	Deletions int      `json:"deletions"`
}

type ImpactHit struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Line    string `json:"line"`
}

type ImpactFile struct {
	Path      string `json:"path"`
	Layer     string `json:"layer,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
}
