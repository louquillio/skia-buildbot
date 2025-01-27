// Package simple_baseliner houses an implementation of BaselineFetcher that directly
// interfaces with a ExpectationsStore.
package simple_baseliner

import (
	"go.skia.org/infra/go/skerr"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/golden/go/baseline"
	"go.skia.org/infra/golden/go/expstorage"
)

// The SimpleBaselineFetcher is an implementation of BaselineFetcher that directly
// interfaces with the ExpectationsStore to retrieve the baselines.
// Reminder that baselines are the set of current expectations, but only
// the positive images.
type SimpleBaselineFetcher struct {
	exp expstorage.ExpectationsStore
}

// New returns an instance of SimpleBaselineFetcher. The passed in ExpectationsStore
// can/should be read-only.
func New(e expstorage.ExpectationsStore) *SimpleBaselineFetcher {
	return &SimpleBaselineFetcher{
		exp: e,
	}
}

// FetchBaseline implements the BaselineFetcher interface.
func (f *SimpleBaselineFetcher) FetchBaseline(clID string, crs string, issueOnly bool) (*baseline.Baseline, error) {
	if clID == "" {
		exp, err := f.exp.GetCopy()
		if err != nil {
			return nil, skerr.Wrapf(err, "geting master branch expectations")
		}
		b := baseline.Baseline{
			ChangeListID:     "",
			CodeReviewSystem: "",
			Expectations:     exp.AsBaseline(),
		}
		md5Sum, err := util.MD5Sum(b.Expectations)
		if err != nil {
			return nil, skerr.Wrapf(err, "calculating md5 hash of expectations")
		}
		b.MD5 = md5Sum
		return &b, nil
	}

	issueStore := f.exp.ForChangeList(clID, crs)

	iexp, err := issueStore.GetCopy()
	if err != nil {
		return nil, skerr.Wrapf(err, "getting expectations for %s (%s)", clID, crs)
	}
	if issueOnly {
		md5Sum, err := util.MD5Sum(iexp)
		if err != nil {
			return nil, skerr.Wrapf(err, "calculating md5 hash of issue expectations")
		}
		return &baseline.Baseline{
			ChangeListID:     clID,
			CodeReviewSystem: crs,
			Expectations:     iexp.AsBaseline(),
			MD5:              md5Sum,
		}, nil
	}

	exp, err := f.exp.GetCopy()
	if err != nil {
		return nil, skerr.Wrapf(err, "getting master branch expectations")
	}

	exp.MergeExpectations(iexp)

	b := baseline.Baseline{
		ChangeListID:     clID,
		CodeReviewSystem: crs,
		Expectations:     exp.AsBaseline(),
	}
	md5Sum, err := util.MD5Sum(b.Expectations)
	if err != nil {
		return nil, skerr.Wrapf(err, "calculating md5 hash of expectations")
	}
	b.MD5 = md5Sum
	return &b, nil
}

// Make sure SimpleBaselineFetcher fulfills the BaselineFetcher interface
var _ baseline.BaselineFetcher = (*SimpleBaselineFetcher)(nil)
