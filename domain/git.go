package domain

type FileDiff struct {
	Path      string
	Status    string
	Additions int
	Deletions int
}

type GitStatus struct {
	Branch      string
	Clean       bool
	Ahead       int
	Behind      int
	Uncommitted []string
}
