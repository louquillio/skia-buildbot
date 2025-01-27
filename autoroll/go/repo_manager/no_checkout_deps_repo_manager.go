package repo_manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"

	"go.skia.org/infra/autoroll/go/codereview"
	"go.skia.org/infra/autoroll/go/revision"
	"go.skia.org/infra/go/depot_tools"
	"go.skia.org/infra/go/exec"
	"go.skia.org/infra/go/gerrit"
	"go.skia.org/infra/go/gitiles"
	"go.skia.org/infra/go/skerr"
	"go.skia.org/infra/go/util"
)

var (
	// Use this function to instantiate a RepoManager. This is able to be
	// overridden for testing.
	NewNoCheckoutDEPSRepoManager func(context.Context, *NoCheckoutDEPSRepoManagerConfig, string, gerrit.GerritInterface, string, string, *http.Client, codereview.CodeReview, bool) (RepoManager, error) = newNoCheckoutDEPSRepoManager

	getDepRegex = regexp.MustCompile("[a-f0-9]+")
)

// NoCheckoutDEPSRepoManagerConfig provides configuration for RepoManagers which
// don't use a local checkout.
type NoCheckoutDEPSRepoManagerConfig struct {
	NoCheckoutRepoManagerConfig
	// URL of the child repo.
	ChildRepo string `json:"childRepo"` // TODO(borenet): Can we just get this from DEPS?

	// Optional; transitive dependencies to roll. This is a mapping of
	// dependencies of the child repo which are also dependencies of the
	// parent repo and should be rolled at the same time. Keys are paths
	// to transitive dependencies within the child repo (as specified in
	// DEPS), and values are paths to those dependencies within the parent
	// repo.
	TransitiveDeps map[string]string `json:"transitiveDeps"`
}

func (c *NoCheckoutDEPSRepoManagerConfig) Validate() error {
	if err := c.NoCheckoutRepoManagerConfig.Validate(); err != nil {
		return err
	}
	if c.ChildRepo == "" {
		return errors.New("ChildRepo is required.")
	}
	if c.ParentBranch == "" {
		return errors.New("ParentBranch is required.")
	}
	if c.ParentRepo == "" {
		return errors.New("ParentRepo is required.")
	}
	for _, s := range c.PreUploadSteps {
		if _, err := GetPreUploadStep(s); err != nil {
			return err
		}
	}
	return nil
}

type noCheckoutDEPSRepoManager struct {
	*noCheckoutRepoManager
	childRepo      *gitiles.Repo
	childRepoUrl   string
	depotTools     string
	gclient        string
	parentRepoUrl  string
	transitiveDeps map[string]string
}

// newNoCheckoutDEPSRepoManager returns a RepoManager instance which does not use
// a local checkout.
func newNoCheckoutDEPSRepoManager(ctx context.Context, c *NoCheckoutDEPSRepoManagerConfig, workdir string, g gerrit.GerritInterface, recipeCfgFile, serverURL string, client *http.Client, cr codereview.CodeReview, local bool) (RepoManager, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(workdir, os.ModePerm); err != nil {
		return nil, err
	}

	depotTools, err := depot_tools.GetDepotTools(ctx, workdir, recipeCfgFile)
	if err != nil {
		return nil, err
	}

	rv := &noCheckoutDEPSRepoManager{
		childRepo:      gitiles.NewRepo(c.ChildRepo, client),
		childRepoUrl:   c.ChildRepo,
		depotTools:     depotTools,
		gclient:        path.Join(depotTools, GCLIENT),
		parentRepoUrl:  c.ParentRepo,
		transitiveDeps: c.TransitiveDeps,
	}
	ncrm, err := newNoCheckoutRepoManager(ctx, c.NoCheckoutRepoManagerConfig, workdir, g, serverURL, client, cr, rv.createRoll, rv.updateHelper, local)
	if err != nil {
		return nil, err
	}
	rv.noCheckoutRepoManager = ncrm

	return rv, nil
}

// getDEPSFile downloads and returns the path to the DEPS file, and a cleanup
// function to run when finished with it.
func (rm *noCheckoutDEPSRepoManager) getDEPSFile(ctx context.Context, repo *gitiles.Repo, baseCommit string) (rv string, cleanup func(), rvErr error) {
	wd, err := ioutil.TempDir("", "")
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if rvErr != nil {
			util.RemoveAll(wd)
		}
	}()

	// Download the DEPS file from the parent repo.
	buf := bytes.NewBuffer([]byte{})
	if err := repo.ReadFileAtRef(ctx, "DEPS", baseCommit, buf); err != nil {
		return "", nil, err
	}

	// Use "gclient getdep" to retrieve the last roll revision.

	// "gclient getdep" requires a .gclient file.
	if _, err := exec.RunCwd(ctx, wd, "python", rm.gclient, "config", repo.URL); err != nil {
		return "", nil, err
	}
	splitRepo := strings.Split(repo.URL, "/")
	fakeCheckoutDir := path.Join(wd, strings.TrimSuffix(splitRepo[len(splitRepo)-1], ".git"))
	if err := os.Mkdir(fakeCheckoutDir, os.ModePerm); err != nil {
		return "", nil, err
	}
	depsFile := path.Join(fakeCheckoutDir, "DEPS")
	if err := ioutil.WriteFile(depsFile, buf.Bytes(), os.ModePerm); err != nil {
		return "", nil, err
	}
	return depsFile, func() { util.RemoveAll(wd) }, nil
}

// See documentation for noCheckoutRepoManagerCreateRollHelperFunc.
func (rm *noCheckoutDEPSRepoManager) createRoll(ctx context.Context, from, to *revision.Revision, rolling []*revision.Revision, serverURL, cqExtraTrybots string, emails []string) (string, map[string]string, error) {
	rm.infoMtx.RLock()
	defer rm.infoMtx.RUnlock()

	// Download the DEPS file from the parent repo.
	depsFile, cleanup, err := rm.getDEPSFile(ctx, rm.parentRepo, rm.baseCommit)
	if err != nil {
		return "", nil, err
	}
	defer cleanup()

	// Write the new DEPS content.
	if err := rm.setdep(ctx, depsFile, rm.childPath, to.Id); err != nil {
		return "", nil, err
	}

	// Update any transitive DEPS.
	transitiveDeps := []*TransitiveDep{}
	if len(rm.transitiveDeps) > 0 {
		for childPath, parentPath := range rm.transitiveDeps {
			oldRev, err := rm.getdep(ctx, depsFile, parentPath)
			if err != nil {
				return "", nil, err
			}
			newRev, ok := to.Dependencies[childPath]
			if !ok {
				return "", nil, skerr.Fmt("To-revision %s is missing dependency entry for %s", to.Id, childPath)
			}
			if oldRev != newRev {
				if err := rm.setdep(ctx, depsFile, parentPath, newRev); err != nil {
					return "", nil, err
				}
				transitiveDeps = append(transitiveDeps, &TransitiveDep{
					ParentPath:  parentPath,
					RollingFrom: oldRev,
					RollingTo:   newRev,
				})
			}
		}
	}

	// Read the updated DEPS content.
	newDEPSContent, err := ioutil.ReadFile(depsFile)
	if err != nil {
		return "", nil, err
	}

	// Build the commit message.
	commitMsg, err := rm.buildCommitMsg(&CommitMsgVars{
		ChildPath:      rm.childPath,
		ChildRepo:      rm.childRepoUrl,
		CqExtraTrybots: cqExtraTrybots,
		Reviewers:      emails,
		Revisions:      rolling,
		RollingFrom:    from,
		RollingTo:      to,
		ServerURL:      serverURL,
		TransitiveDeps: transitiveDeps,
	})
	if err != nil {
		return "", nil, fmt.Errorf("Failed to build commit msg: %s", err)
	}
	return commitMsg, map[string]string{"DEPS": string(newDEPSContent)}, nil
}

func (rm *noCheckoutDEPSRepoManager) getdep(ctx context.Context, depsFile, depPath string) (string, error) {
	output, err := exec.RunCwd(ctx, path.Dir(depsFile), "python", rm.gclient, "getdep", "-r", depPath)
	if err != nil {
		return "", err
	}
	splitGetdep := strings.Split(strings.TrimSpace(output), "\n")
	rev := strings.TrimSpace(splitGetdep[len(splitGetdep)-1])
	if getDepRegex.MatchString(rev) {
		if len(rev) == 40 {
			return rev, nil
		}
		// The DEPS entry may be a shortened commit hash. Try to resolve
		// the full hash.
		rev, err = rm.childRepo.ResolveRef(ctx, rev)
		if err != nil {
			return "", skerr.Wrapf(err, "`gclient getdep` produced what appears to be a shortened commit hash, but failed to resolve it as a commit via gitiles. Output of `gclient getdep`:\n%s", output)
		}
		return rev, nil
	}
	return "", fmt.Errorf("Got invalid output for `gclient getdep`: %s", output)
}

func (rm *noCheckoutDEPSRepoManager) setdep(ctx context.Context, depsFile, depPath, rev string) error {
	args := []string{"setdep", "-r", fmt.Sprintf("%s@%s", depPath, rev)}
	_, err := exec.RunCommand(ctx, &exec.Command{
		Dir:  path.Dir(depsFile),
		Env:  depot_tools.Env(rm.depotTools),
		Name: rm.gclient,
		Args: args,
	})
	return err
}

// See documentation for noCheckoutRepoManagerUpdateHelperFunc.
func (rm *noCheckoutDEPSRepoManager) updateHelper(ctx context.Context, parentRepo *gitiles.Repo, baseCommit string) (*revision.Revision, *revision.Revision, []*revision.Revision, error) {
	rm.infoMtx.Lock()
	defer rm.infoMtx.Unlock()

	// Find the last roll rev.
	depsFile, cleanup, err := rm.getDEPSFile(ctx, rm.parentRepo, baseCommit)
	if err != nil {
		return nil, nil, nil, err
	}
	defer cleanup()
	lastRollHash, err := rm.getdep(ctx, depsFile, rm.childPath)
	if err != nil {
		return nil, nil, nil, err
	}
	lastRollDetails, err := rm.childRepo.Details(ctx, lastRollHash)
	if err != nil {
		return nil, nil, nil, err
	}
	lastRollRev := revision.FromLongCommit(rm.childRevLinkTmpl, lastRollDetails)

	// Get the tip-of-tree revision.
	tipRevDetails, err := rm.childRepo.Details(ctx, rm.childBranch)
	if err != nil {
		return nil, nil, nil, err
	}
	tipRev := revision.FromLongCommit(rm.childRevLinkTmpl, tipRevDetails)

	// Find the not-yet-rolled child repo commits.
	// Only consider commits on the "main" branch as roll candidates.
	notRolled, err := rm.childRepo.LogFirstParent(ctx, lastRollRev.Id, tipRev.Id)
	if err != nil {
		return nil, nil, nil, err
	}
	notRolledRevs := revision.FromLongCommits(rm.childRevLinkTmpl, notRolled)

	// Transitive deps.
	if len(rm.transitiveDeps) > 0 {
		for _, rev := range append(notRolledRevs, tipRev, lastRollRev) {
			childDepsFile, childCleanup, err := rm.getDEPSFile(ctx, rm.childRepo, rev.Id)
			if err != nil {
				return nil, nil, nil, err
			}
			defer childCleanup()
			for childPath := range rm.transitiveDeps {
				childRev, err := rm.getdep(ctx, childDepsFile, childPath)
				if err != nil {
					return nil, nil, nil, err
				}
				if rev.Dependencies == nil {
					rev.Dependencies = map[string]string{}
				}
				rev.Dependencies[childPath] = childRev
			}
		}
	}

	return lastRollRev, tipRev, notRolledRevs, nil
}

// See documentation for RepoManager interface.
func (r *noCheckoutDEPSRepoManager) GetRevision(ctx context.Context, id string) (*revision.Revision, error) {
	details, err := r.childRepo.Details(ctx, id)
	if err != nil {
		return nil, err
	}
	return revision.FromLongCommit(r.childRevLinkTmpl, details), nil
}
