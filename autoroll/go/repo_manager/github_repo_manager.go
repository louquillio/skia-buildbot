package repo_manager

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"

	"cloud.google.com/go/storage"
	"go.skia.org/infra/autoroll/go/codereview"
	"go.skia.org/infra/autoroll/go/revision"
	"go.skia.org/infra/go/gcs"
	"go.skia.org/infra/go/gcs/gcsclient"
	"go.skia.org/infra/go/git"
	"go.skia.org/infra/go/github"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/go/vcsinfo"
)

const (
	TMPL_COMMIT_MSG_GITHUB = `Roll {{.ChildPath}} {{.RollingFrom.String}}..{{.RollingTo.String}} ({{len .Revisions}} commits)

{{.ChildRepo}}/compare/{{.RollingFrom.String}}...{{.RollingTo.String}}

{{if .IncludeLog}}git log {{.RollingFrom}}..{{.RollingTo}} --first-parent --oneline
{{range .Revisions}}{{.Timestamp.Format "2006-01-02"}} {{.Author}} {{.Description}}
{{end}}{{end}}{{if len .TransitiveDeps}}
Also rolling transitive DEPS:
{{range .TransitiveDeps}}  {{.ParentPath}} {{.RollingFrom}}..{{.RollingTo}}
{{end}}{{end}}

If this roll has caused a breakage, revert this CL and stop the roller
using the controls here:
{{.ServerURL}}
Please CC {{stringsJoin .Reviewers ","}} on the revert to ensure that a human
is aware of the problem.

To report a problem with the AutoRoller itself, please file a bug:
https://bugs.chromium.org/p/skia/issues/entry?template=Autoroller+Bug

Documentation for the AutoRoller is here:
https://skia.googlesource.com/buildbot/+/master/autoroll/README.md

`
)

var (
	// Use this function to instantiate a NewGithubRepoManager. This is able to be
	// overridden for testing.
	NewGithubRepoManager func(context.Context, *GithubRepoManagerConfig, string, *github.GitHub, string, string, *http.Client, codereview.CodeReview, bool) (RepoManager, error) = newGithubRepoManager

	pullRequestInLogRE = regexp.MustCompile(`(?m) \((#[0-9]+)\)$`)
)

// GithubRepoManagerConfig provides configuration for the Github RepoManager.
type GithubRepoManagerConfig struct {
	CommonRepoManagerConfig
	// URL of the child repo.
	ChildRepoURL string `json:"childRepoURL"`
	// The roller will update this file with the child repo's revision.
	RevisionFile string `json:"revisionFile"`
	// GCS bucket to use if filtering revisions by presence of files in GCS.
	StorageBucket string `json:"storageBucket"`
	// Templates of GCS files to look for; if provided, revisions to roll
	// will be filtered by the presence of these files in GCS.
	StoragePathTemplates []string `json:"storagePathTemplates"`
}

// githubRepoManager is a struct used by the autoroller for managing checkouts.
type githubRepoManager struct {
	*commonRepoManager
	filterRevisionsByGCS []string
	gcs                  gcs.GCSClient
	githubClient         *github.GitHub
	parentRepo           *git.Checkout
	parentRepoURL        string
	childRepoURL         string
	revisionFile         string
	gsBucket             string
	gsPathTemplates      []string
}

// newGithubRepoManager returns a RepoManager instance which operates in the given
// working directory and updates at the given frequency.
func newGithubRepoManager(ctx context.Context, c *GithubRepoManagerConfig, workdir string, githubClient *github.GitHub, recipeCfgFile, serverURL string, client *http.Client, cr codereview.CodeReview, local bool) (RepoManager, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	wd := path.Join(workdir, "github_repos")
	if _, err := os.Stat(wd); err != nil {
		if err := os.MkdirAll(wd, 0755); err != nil {
			return nil, err
		}
	}

	// Create and populate the parent directory if needed.
	_, repo := GetUserAndRepo(c.ParentRepo)
	userFork := fmt.Sprintf("git@github.com:%s/%s.git", cr.UserName(), repo)
	parentRepo, err := git.NewCheckout(ctx, userFork, wd)
	if err != nil {
		return nil, err
	}

	if c.CommitMsgTmpl == "" {
		c.CommitMsgTmpl = TMPL_COMMIT_MSG_GITHUB
	}
	crm, err := newCommonRepoManager(ctx, c.CommonRepoManagerConfig, wd, serverURL, nil, client, cr, local)
	if err != nil {
		return nil, err
	}

	// Create and populate the child directory if needed.
	if _, err := os.Stat(crm.childDir); err != nil {
		if err := os.MkdirAll(crm.childDir, 0755); err != nil {
			return nil, err
		}
		if _, err := git.GitDir(crm.childDir).Git(ctx, "clone", c.ChildRepoURL, "."); err != nil {
			return nil, err
		}
	}

	var gcsClient gcs.GCSClient
	if len(c.StoragePathTemplates) > 0 {
		storageClient, err := storage.NewClient(ctx)
		if err != nil {
			return nil, err
		}
		gcsClient = gcsclient.New(storageClient, c.StorageBucket)
	}

	gr := &githubRepoManager{
		commonRepoManager: crm,
		gcs:               gcsClient,
		githubClient:      githubClient,
		parentRepo:        parentRepo,
		parentRepoURL:     c.ParentRepo,
		childRepoURL:      c.ChildRepoURL,
		revisionFile:      c.RevisionFile,
		gsBucket:          c.StorageBucket,
		gsPathTemplates:   c.StoragePathTemplates,
	}

	return gr, nil
}

// See documentation for RepoManager interface.
func (rm *githubRepoManager) Update(ctx context.Context) (*revision.Revision, *revision.Revision, []*revision.Revision, error) {
	// Sync the projects.
	rm.repoMtx.Lock()
	defer rm.repoMtx.Unlock()

	// Update the repositories.
	if err := rm.parentRepo.Update(ctx); err != nil {
		return nil, nil, nil, err
	}
	if err := rm.childRepo.Update(ctx); err != nil {
		return nil, nil, nil, err
	}

	// Check to see whether there is an upstream yet.
	remoteOutput, err := rm.parentRepo.Git(ctx, "remote", "show")
	if err != nil {
		return nil, nil, nil, err
	}
	remoteFound := false
	remoteLines := strings.Split(remoteOutput, "\n")
	for _, remoteLine := range remoteLines {
		if remoteLine == GITHUB_UPSTREAM_REMOTE_NAME {
			remoteFound = true
			break
		}
	}
	if !remoteFound {
		if _, err := rm.parentRepo.Git(ctx, "remote", "add", GITHUB_UPSTREAM_REMOTE_NAME, rm.parentRepoURL); err != nil {
			return nil, nil, nil, err
		}
	}
	// Fetch upstream.
	if _, err := rm.parentRepo.Git(ctx, "fetch", GITHUB_UPSTREAM_REMOTE_NAME, rm.parentBranch); err != nil {
		return nil, nil, nil, err
	}

	// Read the contents of the revision file to determine the last roll rev.
	revisionFileContents, err := rm.githubClient.ReadRawFile(rm.parentBranch, rm.revisionFile)
	if err != nil {
		return nil, nil, nil, err
	}
	lastRollHash := strings.TrimRight(revisionFileContents, "\n")
	lastRollDetails, err := rm.childRepo.Details(ctx, lastRollHash)
	if err != nil {
		return nil, nil, nil, err
	}
	lastRollRev := revision.FromLongCommit(rm.childRevLinkTmpl, lastRollDetails)

	// Get the tip-of-tree revision. Because we filter the notRolledRevs,
	// this may not end up being present in that list.
	tipRev, err := rm.getTipRev(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	// Find the not-rolled child repo commits.
	notRolledRevs, err := rm.getCommitsNotRolled(ctx, lastRollRev, tipRev)
	if err != nil {
		return nil, nil, nil, err
	}

	// Optionally filter not-rolled revisions by the existence of matching
	// files in GCS.
	if len(rm.gsPathTemplates) > 0 {
		if len(notRolledRevs) > 0 {
			filtered := make([]*revision.Revision, 0, len(notRolledRevs))
			for _, notRolledRev := range notRolledRevs {
				// Check to see if this commit exists in the gsPath locations.
				missingFile := false
				for _, gsPathTemplate := range rm.gsPathTemplates {
					gsPath := fmt.Sprintf(gsPathTemplate, notRolledRev.Id)
					fileExists, err := rm.gcs.DoesFileExist(ctx, gsPath)
					if err != nil {
						return nil, nil, nil, err
					}
					if fileExists {
						sklog.Infof("[gcsFileFilter] Found %s", gsPath)
						continue
					} else {
						sklog.Infof("[gcsFileFilter] Could not find %s", gsPath)
						missingFile = true
						break
					}
				}
				if !missingFile {
					sklog.Infof("[gcsFileFilter] Found all %s paths for %s", rm.gsPathTemplates, notRolledRev.Id)
					filtered = append(filtered, notRolledRev)
				}
			}
			// We may have filtered out tipRev. For consistency's sake, it
			// needs to be in notRolledRevs, so pick the latest rev and use
			// that.
			if len(filtered) == 0 {
				sklog.Warningf("Filtered out tip revision %s, and no notRolledRevs remain; using last roll rev %s", tipRev.Id, lastRollRev.Id)
				tipRev = lastRollRev
			} else if filtered[0].Id != tipRev.Id {
				sklog.Warningf("Filtered out tip revision %s; using %s instead.", tipRev.Id, filtered[0].Id)
				tipRev = filtered[0]
			}
			notRolledRevs = filtered
		}
	}

	return lastRollRev, tipRev, notRolledRevs, nil
}

func (rm *githubRepoManager) cleanParent(ctx context.Context) error {
	if _, err := rm.parentRepo.Git(ctx, "clean", "-d", "-f", "-f"); err != nil {
		return err
	}
	_, _ = rm.parentRepo.Git(ctx, "rebase", "--abort")
	if _, err := rm.parentRepo.Git(ctx, "checkout", fmt.Sprintf("%s/%s", GITHUB_UPSTREAM_REMOTE_NAME, rm.parentBranch), "-f"); err != nil {
		return err
	}
	_, _ = rm.parentRepo.Git(ctx, "branch", "-D", ROLL_BRANCH)
	return nil
}

// See documentation for RepoManager interface.
func (rm *githubRepoManager) CreateNewRoll(ctx context.Context, from, to *revision.Revision, rolling []*revision.Revision, emails []string, cqExtraTrybots string, dryRun bool) (int64, error) {
	rm.repoMtx.Lock()
	defer rm.repoMtx.Unlock()

	sklog.Info("Creating a new Github Roll")

	// Clean the checkout, get onto a fresh branch.
	if err := rm.cleanParent(ctx); err != nil {
		return 0, err
	}
	if _, err := rm.parentRepo.Git(ctx, "checkout", fmt.Sprintf("%s/%s", GITHUB_UPSTREAM_REMOTE_NAME, rm.parentBranch), "-b", ROLL_BRANCH); err != nil {
		return 0, err
	}
	// Defer cleanup.
	defer func() {
		util.LogErr(rm.cleanParent(ctx))
	}()

	// Make sure the forked repo is at the same hash as the target repo before
	// creating the pull request.
	if _, err := rm.parentRepo.Git(ctx, "push", "origin", ROLL_BRANCH, "-f"); err != nil {
		return 0, err
	}

	// Make sure the right name and email are set.
	if !rm.local {
		if _, err := rm.parentRepo.Git(ctx, "config", "user.name", rm.codereview.UserName()); err != nil {
			return 0, err
		}
		if _, err := rm.parentRepo.Git(ctx, "config", "user.email", rm.codereview.UserEmail()); err != nil {
			return 0, err
		}
	}

	// Build the commit message.
	user, repo := GetUserAndRepo(rm.childRepoURL)
	details := make([]*vcsinfo.LongCommit, 0, len(rolling))
	for _, c := range rolling {
		d, err := rm.childRepo.Details(ctx, c.Id)
		if err != nil {
			return 0, fmt.Errorf("Failed to get commit details: %s", err)
		}
		// Github autolinks PR numbers to be of the same repository in logStr. Fix this by
		// explicitly adding the child repo to the PR number.
		d.Subject = pullRequestInLogRE.ReplaceAllString(d.Subject, fmt.Sprintf(" (%s/%s$1)", user, repo))
		d.Body = pullRequestInLogRE.ReplaceAllString(d.Body, fmt.Sprintf(" (%s/%s$1)", user, repo))
		details = append(details, d)
	}

	commitMsg, err := rm.buildCommitMsg(&CommitMsgVars{
		ChildPath:   rm.childPath,
		ChildRepo:   rm.childRepoURL,
		Reviewers:   emails,
		Revisions:   rolling,
		RollingFrom: from,
		RollingTo:   to,
		ServerURL:   rm.serverURL,
	})

	for i := len(details) - 1; i >= 0; i-- {
		// Write the file.
		if err := ioutil.WriteFile(path.Join(rm.parentRepo.Dir(), rm.revisionFile), []byte(details[i].Hash+"\n"), os.ModePerm); err != nil {
			return 0, err
		}

		// Commit.
		msg := fmt.Sprintf("%s %s", details[i].Hash[:9], details[i].Subject)
		if _, err := rm.parentRepo.Git(ctx, "commit", "-a", "-m", msg); err != nil {
			return 0, err
		}
	}

	// Run the pre-upload steps.
	for _, s := range rm.preUploadSteps {
		if err := s(ctx, nil, rm.httpClient, rm.parentRepo.Dir()); err != nil {
			return 0, fmt.Errorf("Error when running pre-upload step: %s", err)
		}
	}

	// Push to the forked repository.
	if _, err := rm.parentRepo.Git(ctx, "push", "origin", ROLL_BRANCH, "-f"); err != nil {
		return 0, err
	}

	// Grab the first line of the commit msg to use as the title of the pull request.
	title := strings.Split(commitMsg, "\n")[0]
	// Use the remaining part of the commit message as the pull request description.
	commitMsgLines := strings.Split(commitMsg, "\n")
	var descComment []string
	if len(commitMsgLines) > 50 {
		// Truncate too large description comment because Github API cannot handle large comments.
		descComment = commitMsgLines[1:50]
		descComment = append(descComment, "...")
	} else {
		descComment = commitMsgLines[1:]
	}
	// Create a pull request.
	headBranch := fmt.Sprintf("%s:%s", rm.codereview.UserName(), ROLL_BRANCH)
	pr, err := rm.githubClient.CreatePullRequest(title, rm.parentBranch, headBranch, strings.Join(descComment, "\n"))
	if err != nil {
		return 0, err
	}

	// Add appropriate label to the pull request.
	label := github.COMMIT_LABEL
	if dryRun {
		label = github.DRYRUN_LABEL
	}
	if err := rm.githubClient.AddLabel(pr.GetNumber(), label); err != nil {
		return 0, err
	}

	return int64(pr.GetNumber()), nil
}

func GetUserAndRepo(githubRepo string) (string, string) {
	repoTokens := strings.Split(githubRepo, ":")
	user := strings.Split(repoTokens[1], "/")[0]
	repo := strings.TrimRight(strings.Split(repoTokens[1], "/")[1], ".git")
	return user, repo
}
