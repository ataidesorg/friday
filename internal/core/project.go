package core

// Project is a repository or directory Friday works in.
type Project struct {
	ID               ProjectID `json:"id"`
	Name             string    `json:"name"`
	Root             string    `json:"root"`
	InstructionFiles []string  `json:"instruction_files,omitempty"`
	// GlobalInstructionFiles are absolute paths wired by the CLI from the
	// Friday home (never from project config), so they bypass root confinement.
	GlobalInstructionFiles []string          `json:"global_instruction_files,omitempty"`
	Commands               map[string]string `json:"commands,omitempty"`
	VCS                    *VCSInfo          `json:"vcs,omitempty"`
}

// VCSInfo describes the version-control state of a project root.
type VCSInfo struct {
	Kind   string `json:"kind"`
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
	Dirty  bool   `json:"dirty"`
}

// WorkspaceKind says how isolated a workspace is from the primary checkout.
type WorkspaceKind string

// Workspace kinds.
const (
	WorkspacePrimary   WorkspaceKind = "primary"
	WorkspaceWorktree  WorkspaceKind = "worktree"
	WorkspaceEphemeral WorkspaceKind = "ephemeral"
)

// Workspace is the directory a run mutates.
type Workspace struct {
	ID      WorkspaceID   `json:"id"`
	Project ProjectID     `json:"project"`
	Root    string        `json:"root"`
	Kind    WorkspaceKind `json:"kind"`
	Branch  string        `json:"branch,omitempty"`
}
