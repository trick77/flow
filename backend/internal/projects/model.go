package projects

import "time"

// Project is a named group of GitHub repositories with one sandbox.
//
// Table: projects
type Project struct {
	ID        string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Repo is one repository of a project.
//
// Table: project_repos
type Repo struct {
	ID            string
	ProjectID     string
	FullName      string
	DefaultBranch string
	Position      int
	CreatedAt     time.Time
}
