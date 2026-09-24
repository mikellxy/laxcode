package tools

import "context"

// SkillPackageFile is a normalized UTF-8 file inside one skill directory.
// Path always uses slash-separated, package-relative syntax.
type SkillPackageFile struct {
	Path       string
	Content    []byte
	Executable bool
}

// SkillPackageSnapshot is an immutable package view used to plan and compare
// an update before atomically replacing the directory.
type SkillPackageSnapshot struct {
	Files    []SkillPackageFile
	Revision string
}

// SkillPackageStore is the filesystem boundary for global skill mutations.
// Implementations must confine every operation to their configured skills root
// and reject symlinks or non-regular package entries.
type SkillPackageStore interface {
	Load(context.Context, string) (SkillPackageSnapshot, error)
	Create(context.Context, string, []SkillPackageFile) error
	Replace(context.Context, string, string, []SkillPackageFile) error
}
