// Package code_review defines some types for getting data into and out of
// Code Review Systems (e.g. Gerrit, GitHub).
package code_review

import (
	"context"
	"errors"
	"time"

	"go.skia.org/infra/go/vcsinfo"
)

// The Client interface is an abstraction around a Code Review System
type Client interface {
	// GetChangeList returns the ChangeList corresponding to the given id.
	// Returns ErrNotFound if it doesn't exist.
	GetChangeList(ctx context.Context, id string) (ChangeList, error)

	// GetPatchSets returns the PatchSets belonging to the ChangeList with the ID
	// in index order (see PatchSet.Order).
	// Returns ErrNotFound if the ChangeList doesn't exist.
	GetPatchSets(ctx context.Context, clID string) ([]PatchSet, error)

	// GetChangeListForCommit returns the ChangeList corresponding to the given git commit.
	// Returns ErrNotFound if it doesn't exist.
	GetChangeListForCommit(ctx context.Context, commit *vcsinfo.LongCommit) (ChangeList, error)

	// Returns the underlying system (e.g. "gerrit")
	System() string
}

// The Updater interface is an abstraction around the code that tracks ChangeLists which land.
type Updater interface {
	// UpdateChangeListsAsLanded goes through the given commits and marks any ChangeList
	// objects as Landed. For those that are marked as landed, it should update the master
	// branch's Expectations as well.
	UpdateChangeListsAsLanded(ctx context.Context, commits []*vcsinfo.LongCommit) error
}

var ErrNotFound = errors.New("not found")

type ChangeList struct {
	// SystemID is expected to be unique between all ChangeLists for a given system and repo.
	SystemID string

	Owner   string
	Status  CLStatus
	Subject string
	Updated time.Time
}

type CLStatus int

const (
	Open CLStatus = iota
	Abandoned
	Landed
)

func (c CLStatus) String() string {
	switch c {
	case Open:
		return "Open"
	case Abandoned:
		return "Abandoned"
	case Landed:
		return "Landed"
	}
	return "<unknown>"
}

type PatchSet struct {
	// SystemID may or may not be unique for all PatchSets for a given system.
	// Definitely unique for a given ChangeList.
	SystemID string

	// ChangeListID is the id that the PatchSet belongs to.
	ChangeListID string
	// It is convenient to think about PatchSets starting at 1 and increasing
	// monotonically. This gives some measure of time/progress. Order is the
	// index of this PatchSet relative to all other PatchSets on this CL.
	Order   int
	GitHash string
}
