package roller

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"go.skia.org/infra/autoroll/go/codereview"
	"go.skia.org/infra/autoroll/go/manual"
	"go.skia.org/infra/autoroll/go/modes"
	arb_notifier "go.skia.org/infra/autoroll/go/notifier"
	"go.skia.org/infra/autoroll/go/recent_rolls"
	"go.skia.org/infra/autoroll/go/repo_manager"
	"go.skia.org/infra/autoroll/go/revision"
	"go.skia.org/infra/autoroll/go/state_machine"
	"go.skia.org/infra/autoroll/go/status"
	"go.skia.org/infra/autoroll/go/strategy"
	"go.skia.org/infra/autoroll/go/time_window"
	"go.skia.org/infra/autoroll/go/unthrottle"
	"go.skia.org/infra/go/autoroll"
	"go.skia.org/infra/go/chatbot"
	"go.skia.org/infra/go/cleanup"
	"go.skia.org/infra/go/comment"
	"go.skia.org/infra/go/email"
	"go.skia.org/infra/go/gcs"
	"go.skia.org/infra/go/gerrit"
	"go.skia.org/infra/go/github"
	"go.skia.org/infra/go/human"
	"go.skia.org/infra/go/metrics2"
	"go.skia.org/infra/go/notifier"
	"go.skia.org/infra/go/skerr"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/util"
)

const (
	AUTOROLL_URL_PUBLIC  = "https://autoroll.skia.org"
	AUTOROLL_URL_PRIVATE = "https://skia-autoroll.corp.goog"

	// Maximum number of not-yet-rolled revisions to store in the DB.
	MAX_NOT_ROLLED_REVS = 50

	// We'll send a notification if this many rolls fail in a row.
	NOTIFY_IF_LAST_N_FAILED = 3
)

// AutoRoller is a struct which automates the merging new revisions of one
// project into another.
type AutoRoller struct {
	cfg             AutoRollerConfig
	childName       string
	codereview      codereview.CodeReview
	currentRoll     codereview.RollImpl
	emails          []string
	emailsMtx       sync.RWMutex
	failureThrottle *state_machine.Throttler
	lastRollRev     *revision.Revision
	liveness        metrics2.Liveness
	manualRollDB    manual.DB
	modeHistory     *modes.ModeHistory
	nextRollRev     *revision.Revision
	notifier        *arb_notifier.AutoRollNotifier
	notifierConfigs []*notifier.Config
	notRolledRevs   []*revision.Revision
	parentName      string
	recent          *recent_rolls.RecentRolls
	rm              repo_manager.RepoManager
	rollIntoAndroid bool
	roller          string
	runningMtx      sync.Mutex
	safetyThrottle  *state_machine.Throttler
	serverURL       string
	sheriff         []string
	sheriffBackup   []string
	sm              *state_machine.AutoRollStateMachine
	status          *status.AutoRollStatusCache
	statusMtx       sync.RWMutex
	strategy        strategy.NextRollStrategy
	strategyHistory *strategy.StrategyHistory
	strategyMtx     sync.RWMutex // Protects strategy
	successThrottle *state_machine.Throttler
	timeWindow      *time_window.TimeWindow
	tipRev          *revision.Revision
}

// NewAutoRoller returns an AutoRoller instance.
func NewAutoRoller(ctx context.Context, c AutoRollerConfig, emailer *email.GMail, chatBotConfigReader chatbot.ConfigReader, g *gerrit.Gerrit, githubClient *github.GitHub, workdir, recipesCfgFile, serverURL string, gcsClient gcs.GCSClient, client *http.Client, rollerName string, local bool, manualRollDB manual.DB) (*AutoRoller, error) {
	// Validation and setup.
	if err := c.Validate(); err != nil {
		return nil, skerr.Wrapf(err, "Failed to validate config")
	}

	cr, err := c.CodeReview().Init(g, githubClient)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to initialize code review")
	}

	// Create the RepoManager.
	var rm repo_manager.RepoManager
	if c.AndroidRepoManager != nil {
		rm, err = repo_manager.NewAndroidRepoManager(ctx, c.AndroidRepoManager, workdir, g, serverURL, c.ServiceAccount, client, cr, local)
	} else if c.CopyRepoManager != nil {
		rm, err = repo_manager.NewCopyRepoManager(ctx, c.CopyRepoManager, workdir, g, recipesCfgFile, serverURL, client, cr, local)
	} else if c.DEPSRepoManager != nil {
		rm, err = repo_manager.NewDEPSRepoManager(ctx, c.DEPSRepoManager, workdir, g, recipesCfgFile, serverURL, client, cr, local)
	} else if c.FuchsiaSDKAndroidRepoManager != nil {
		rm, err = repo_manager.NewFuchsiaSDKAndroidRepoManager(ctx, c.FuchsiaSDKAndroidRepoManager, workdir, g, serverURL, client, cr, local)
	} else if c.FreeTypeRepoManager != nil {
		rm, err = repo_manager.NewFreeTypeRepoManager(ctx, c.FreeTypeRepoManager, workdir, g, recipesCfgFile, serverURL, client, cr, local)
	} else if c.FuchsiaSDKRepoManager != nil {
		rm, err = repo_manager.NewFuchsiaSDKRepoManager(ctx, c.FuchsiaSDKRepoManager, workdir, g, serverURL, client, cr, local)
	} else if c.GithubRepoManager != nil {
		rm, err = repo_manager.NewGithubRepoManager(ctx, c.GithubRepoManager, workdir, githubClient, recipesCfgFile, serverURL, client, cr, local)
	} else if c.GithubCipdDEPSRepoManager != nil {
		rm, err = repo_manager.NewGithubCipdDEPSRepoManager(ctx, c.GithubCipdDEPSRepoManager, workdir, rollerName, githubClient, recipesCfgFile, serverURL, client, cr, local)
	} else if c.GithubDEPSRepoManager != nil {
		rm, err = repo_manager.NewGithubDEPSRepoManager(ctx, c.GithubDEPSRepoManager, workdir, rollerName, githubClient, recipesCfgFile, serverURL, client, cr, local)
	} else if c.NoCheckoutDEPSRepoManager != nil {
		rm, err = repo_manager.NewNoCheckoutDEPSRepoManager(ctx, c.NoCheckoutDEPSRepoManager, workdir, g, recipesCfgFile, serverURL, client, cr, local)
	} else if c.SemVerGCSRepoManager != nil {
		rm, err = repo_manager.NewSemVerGCSRepoManager(ctx, c.SemVerGCSRepoManager, workdir, g, serverURL, client, cr, local)
	} else {
		return nil, skerr.Fmt("Invalid roller config; no repo manager defined!")
	}
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to initialize repo manager")
	}

	sklog.Info("Creating strategy history.")
	sh, err := strategy.NewStrategyHistory(ctx, rollerName, c.ValidStrategies())
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to create strategy history")
	}
	currentStrategy := sh.CurrentStrategy()
	if currentStrategy == nil {
		// If there's no history, set the initial strategy.
		sklog.Infof("Setting initial strategy for %s to %q", rollerName, c.DefaultStrategy())
		if err := sh.Add(ctx, c.DefaultStrategy(), "AutoRoll Bot", "Setting initial strategy."); err != nil {
			return nil, skerr.Wrapf(err, "Failed to set initial strategy")
		}
		currentStrategy = sh.CurrentStrategy()
	}
	sklog.Info("Setting strategy.")
	strat, err := strategy.GetNextRollStrategy(currentStrategy.Strategy)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to get next roll strategy")
	}

	sklog.Info("Running repo_manager.Update()")
	lastRollRev, tipRev, notRolledRevs, err := rm.Update(ctx)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed initial repo manager update")
	}
	nextRollRev, err := strat.GetNextRollRev(ctx, notRolledRevs)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to obtain next roll rev")
	}
	if nextRollRev == nil {
		nextRollRev = lastRollRev
	}

	sklog.Info("Creating roll history")
	recent, err := recent_rolls.NewRecentRolls(ctx, rollerName)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to create recent rolls DB")
	}
	sklog.Info("Creating mode history")
	mh, err := modes.NewModeHistory(ctx, rollerName)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to create mode history")
	}
	if mh.CurrentMode() == nil {
		sklog.Info("Setting initial mode.")
		if err := mh.Add(ctx, modes.MODE_RUNNING, "AutoRoll Bot", "Setting initial mode."); err != nil {
			return nil, skerr.Wrapf(err, "Failed to set initial mode")
		}
	}

	// Throttling counters.
	sklog.Info("Creating throttlers")
	if c.SafetyThrottle == nil {
		c.SafetyThrottle = SAFETY_THROTTLE_CONFIG_DEFAULT
	}
	safetyThrottle, err := state_machine.NewThrottler(ctx, gcsClient, rollerName+"/attempt_counter", c.SafetyThrottle.TimeWindow, c.SafetyThrottle.AttemptCount)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to create safety throttler")
	}

	failureThrottle, err := state_machine.NewThrottler(ctx, gcsClient, rollerName+"/fail_counter", time.Hour, 1)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to create failure throttler")
	}

	var maxRollFreq time.Duration
	if c.MaxRollFrequency != "" {
		maxRollFreq, err = human.ParseDuration(c.MaxRollFrequency)
		if err != nil {
			return nil, skerr.Wrapf(err, "Failed to parse maxRollFrequency")
		}
	}
	successThrottle, err := state_machine.NewThrottler(ctx, gcsClient, rollerName+"/success_counter", maxRollFreq, 1)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to create success throttler")
	}
	sklog.Info("Getting sheriff")
	emails, err := getSheriff(c.ParentName, c.ChildName, c.RollerName, c.Sheriff, c.SheriffBackup)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to get sheriff")
	}
	sklog.Info("Creating notifier")
	configCopies := replaceSheriffPlaceholder(c.Notifiers, emails)
	n, err := arb_notifier.New(ctx, c.ChildName, c.ParentName, serverURL, client, emailer, chatBotConfigReader, configCopies)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to create notifier")
	}
	sklog.Info("Creating status cache.")
	statusCache, err := status.NewCache(ctx, rollerName)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to create status cache")
	}
	sklog.Info("Creating TimeWindow.")
	var tw *time_window.TimeWindow
	if c.TimeWindow != "" {
		tw, err = time_window.Parse(c.TimeWindow)
		if err != nil {
			return nil, skerr.Wrapf(err, "Failed to create TimeWindow")
		}
	}
	arb := &AutoRoller{
		cfg:             c,
		codereview:      cr,
		emails:          emails,
		failureThrottle: failureThrottle,
		lastRollRev:     lastRollRev,
		liveness:        metrics2.NewLiveness("last_autoroll_landed", map[string]string{"roller": c.RollerName}),
		manualRollDB:    manualRollDB,
		modeHistory:     mh,
		nextRollRev:     nextRollRev,
		notifier:        n,
		notifierConfigs: c.Notifiers,
		notRolledRevs:   notRolledRevs,
		recent:          recent,
		rm:              rm,
		roller:          rollerName,
		safetyThrottle:  safetyThrottle,
		serverURL:       serverURL,
		sheriff:         c.Sheriff,
		sheriffBackup:   c.SheriffBackup,
		status:          statusCache,
		strategy:        strat,
		strategyHistory: sh,
		successThrottle: successThrottle,
		timeWindow:      tw,
		tipRev:          tipRev,
	}
	sklog.Info("Creating state machine")
	sm, err := state_machine.New(ctx, arb, n, gcsClient, rollerName)
	if err != nil {
		return nil, skerr.Wrapf(err, "Failed to create state machine")
	}
	arb.sm = sm
	current := recent.CurrentRoll()
	if current != nil {
		rollingTo, err := arb.getRevision(ctx, current.RollingTo)
		if err != nil {
			return nil, err
		}
		roll, err := arb.retrieveRoll(ctx, current, rollingTo)
		if err != nil {
			return nil, skerr.Wrapf(err, "Failed to retrieve current roll")
		}
		arb.currentRoll = roll
	}
	sklog.Info("Done creating autoroller")
	return arb, nil
}

// Retrieve a RollImpl based on the given AutoRollIssue. The passed-in
// AutoRollIssue becomes owned by the RollImpl; it may modify it, insert it
// into the RecentRolls DB, etc. The Issue field is required, and if the roll
// has not yet been inserted into the DB, the RollingFrom, and RollingTo fields
// must be set as well.
func (r *AutoRoller) retrieveRoll(ctx context.Context, roll *autoroll.AutoRollIssue, rollingTo *revision.Revision) (codereview.RollImpl, error) {
	return r.codereview.RetrieveRoll(ctx, roll, r.recent, rollingTo, r.rollFinished)
}

// isSyncError returns true iff the error looks like a sync error.
func isSyncError(err error) bool {
	// TODO(borenet): Remove extra logging.
	sklog.Infof("Encountered error: %q", err.Error())
	if strings.Contains(err.Error(), "Invalid revision range") {
		// Not really an error in the sync itself but indicates that
		// the repo is not up to date, likely due to a server frontend
		// lagging behind.
		sklog.Infof("Is sync error (invalid revision range)")
		return true
	} else if strings.Contains(err.Error(), "The remote end hung up unexpectedly") {
		sklog.Infof("Is sync error (remote hung up)")
		return true
	} else if strings.Contains(err.Error(), "remote error: internal server error") {
		sklog.Infof("Is sync error (internal server error)")
		return true
	} else if strings.Contains(err.Error(), "The requested URL returned error: 502") {
		sklog.Infof("Is sync error (URL returned 502)")
		return true
	} else if strings.Contains(err.Error(), "fatal: bad object") {
		// Not really an error in the sync itself but indicates that
		// the repo is not up to date, likely due to a server frontend
		// lagging behind.
		sklog.Infof("Is sync error (bad object)")
		return true
	}
	sklog.Infof("Not a sync error.")
	return false
}

// Start initiates the AutoRoller's loop.
func (r *AutoRoller) Start(ctx context.Context, tickFrequency, repoFrequency time.Duration) {
	sklog.Infof("Starting autoroller.")
	repo_manager.Start(ctx, r.rm, repoFrequency)
	lv := metrics2.NewLiveness("last_successful_autoroll_tick", map[string]string{"roller": r.roller})
	cleanup.Repeat(tickFrequency, func(_ context.Context) {
		// Explicitly ignore the passed-in context; this allows us to
		// continue running even if the context is canceled, which helps
		// to prevent errors due to interrupted syncs, etc.
		ctx := context.Background()
		if err := r.Tick(ctx); err != nil {
			// Hack: we frequently get failures from GoB which trigger error-rate alerts.
			// These alerts are noise and sometimes hide real failures. If the error is
			// due to a sync failure, log it as a warning instead of an error. We'll rely
			// on the liveness alert in the case where we see persistent sync failures.
			if isSyncError(err) {
				sklog.Warningf("Failed to run autoroll: %s", err)
			} else {
				sklog.Errorf("Failed to run autoroll: %s", err)
			}
		} else {
			lv.Reset()
		}
	}, nil)

	// Update the current sheriff in a loop.
	cleanup.Repeat(30*time.Minute, func(ctx context.Context) {
		emails, err := getSheriff(r.cfg.ParentName, r.cfg.ChildName, r.cfg.RollerName, r.cfg.Sheriff, r.cfg.SheriffBackup)
		if err != nil {
			sklog.Errorf("Failed to retrieve current sheriff: %s", err)
		} else {
			r.emailsMtx.Lock()
			defer r.emailsMtx.Unlock()
			r.emails = emails

			configCopies := replaceSheriffPlaceholder(r.notifierConfigs, emails)
			if err := r.notifier.ReloadConfigs(ctx, configCopies); err != nil {
				sklog.Errorf("Failed to reload configs: %s", err)
				return
			}
		}
	}, nil)

	// Handle requests for manual rolls.
	if r.cfg.SupportsManualRolls {
		lvManualRolls := metrics2.NewLiveness("last_successful_manual_roll_check", map[string]string{"roller": r.roller})
		cleanup.Repeat(time.Minute, func(_ context.Context) {
			// Explicitly ignore the passed-in context; this allows
			// us to continue handling manual rolls even if the
			// context is canceled, which helps to prevent errors
			// due to interrupted syncs, etc.
			ctx := context.Background()
			if err := r.handleManualRolls(ctx); err != nil {
				sklog.Error(err)
			} else {
				lvManualRolls.Reset()
			}
		}, nil)
	}
}

// Utility for replacing the placeholder $SHERIFF with real sheriff emails
// in configs. A modified copy of the passed in configs are returned.
func replaceSheriffPlaceholder(configs []*notifier.Config, emails []string) []*notifier.Config {
	configCopies := []*notifier.Config{}
	for _, n := range configs {
		configCopy := n.Copy()
		if configCopy.Email != nil {
			newEmails := []string{}
			for _, e := range configCopy.Email.Emails {
				if e == "$SHERIFF" {
					newEmails = append(newEmails, emails...)
				} else {
					newEmails = append(newEmails, e)
				}
			}
			configCopy.Email.Emails = newEmails
		}
		configCopies = append(configCopies, configCopy)
	}
	return configCopies
}

// See documentation for state_machine.AutoRollerImpl interface.
func (r *AutoRoller) GetActiveRoll() state_machine.RollCLImpl {
	return r.currentRoll
}

// GetEmails returns the list of email addresses which are copied on rolls.
func (r *AutoRoller) GetEmails() []string {
	r.emailsMtx.RLock()
	defer r.emailsMtx.RUnlock()
	rv := make([]string, len(r.emails))
	copy(rv, r.emails)
	return rv
}

// See documentation for state_machine.AutoRollerImpl interface.
func (r *AutoRoller) GetMode() string {
	return r.modeHistory.CurrentMode().Mode
}

// Reset all of the roller's throttle timers.
func (r *AutoRoller) unthrottle(ctx context.Context) error {
	if err := r.failureThrottle.Reset(ctx); err != nil {
		return err
	}
	if err := r.safetyThrottle.Reset(ctx); err != nil {
		return err
	}
	if err := r.successThrottle.Reset(ctx); err != nil {
		return err
	}
	return nil
}

// See documentation for state_machine.AutoRollerImpl interface.
func (r *AutoRoller) UploadNewRoll(ctx context.Context, from, to *revision.Revision, dryRun bool) (state_machine.RollCLImpl, error) {
	issue, err := r.createNewRoll(ctx, from, to, r.GetEmails(), dryRun)
	if err != nil {
		return nil, err
	}
	roll, err := r.retrieveRoll(ctx, issue, to)
	if err != nil {
		return nil, err
	}
	if err := roll.InsertIntoDB(ctx); err != nil {
		return nil, err
	}
	r.currentRoll = roll
	return roll, nil
}

// createNewRoll is a helper function which uploads a new roll.
func (r *AutoRoller) createNewRoll(ctx context.Context, from, to *revision.Revision, emails []string, dryRun bool) (*autoroll.AutoRollIssue, error) {
	r.statusMtx.RLock()
	var revs []*revision.Revision
	found := false
	for _, rev := range r.notRolledRevs {
		if rev.Id == to.Id {
			found = true
		}
		if found {
			revs = append(revs, rev)
		}
	}
	r.statusMtx.RUnlock()
	issueNum, err := r.rm.CreateNewRoll(ctx, from, to, revs, emails, strings.Join(r.cfg.CqExtraTrybots, ";"), dryRun)
	if err != nil {
		return nil, err
	}
	issue := &autoroll.AutoRollIssue{
		IsDryRun:    dryRun,
		Issue:       issueNum,
		RollingFrom: from.Id,
		RollingTo:   to.Id,
	}
	return issue, nil
}

// Return a state_machine.Throttler indicating that we have failed to roll too many
// times within a time period.
func (r *AutoRoller) FailureThrottle() *state_machine.Throttler {
	return r.failureThrottle
}

// See documentation for state_machine.AutoRollerImpl interface.
func (r *AutoRoller) GetCurrentRev() *revision.Revision {
	r.statusMtx.RLock()
	defer r.statusMtx.RUnlock()
	return r.lastRollRev
}

// See documentation for state_machine.AutoRollerImpl interface.
func (r *AutoRoller) GetNextRollRev() *revision.Revision {
	r.statusMtx.RLock()
	defer r.statusMtx.RUnlock()
	return r.nextRollRev
}

// See documentation for state_machine.AutoRollerImpl interface.
func (r *AutoRoller) InRollWindow(t time.Time) bool {
	return r.timeWindow.Test(t)
}

// See documentation for state_machine.AutoRollerImpl interface.
func (r *AutoRoller) RolledPast(ctx context.Context, rev *revision.Revision) (bool, error) {
	r.statusMtx.RLock()
	defer r.statusMtx.RUnlock()
	// If we've rolled to this rev, then we're past it.
	if rev.Id == r.lastRollRev.Id {
		return true, nil
	}
	// If rev is the nextRollRev, then we aren't past it.
	if rev.Id == r.nextRollRev.Id {
		return false, nil
	}
	// If rev is the tipRev (and we haven't rolled to it), then we aren't
	// past it.
	if rev.Id == r.tipRev.Id {
		return false, nil
	}
	// If rev is any of the notRolledRevs, then we haven't rolled past it.
	for _, notRolledRev := range r.notRolledRevs {
		if rev.Id == notRolledRev.Id {
			return false, nil
		}
	}
	// We don't know about this rev. Assuming the revs we do know about are
	// valid, we must have rolled past this one.
	return true, nil
}

// Return a state_machine.Throttler indicating that we have attempted to upload too
// many CLs within a time period.
func (r *AutoRoller) SafetyThrottle() *state_machine.Throttler {
	return r.safetyThrottle
}

// Return a state_machine.Throttler indicating whether we have successfully rolled too
// many times within a time period.
func (r *AutoRoller) SuccessThrottle() *state_machine.Throttler {
	return r.successThrottle
}

// See documentation for state_machine.AutoRollerImpl interface.
func (r *AutoRoller) UpdateRepos(ctx context.Context) error {
	lastRollRev, tipRev, notRolledRevs, err := r.rm.Update(ctx)
	if err != nil {
		return skerr.Wrap(err)
	}
	r.strategyMtx.RLock()
	defer r.strategyMtx.RUnlock()
	nextRollRev, err := r.strategy.GetNextRollRev(ctx, notRolledRevs)
	if err != nil {
		return skerr.Wrapf(err, "Failed to obtain next roll rev")
	}
	if nextRollRev == nil {
		nextRollRev = lastRollRev
	}
	sklog.Infof("lastRollRev is: %s", lastRollRev.Id)
	sklog.Infof("tipRev is:      %s", tipRev.Id)
	sklog.Infof("nextRollRev is: %s", nextRollRev.Id)
	sklog.Infof("notRolledRevs:  %d", len(notRolledRevs))

	// Sanity checks.
	foundLast := false
	foundTip := false
	foundNext := false
	for _, rev := range notRolledRevs {
		if rev.Id == lastRollRev.Id {
			foundLast = true
		}
		if rev.Id == tipRev.Id {
			foundTip = true
		}
		if rev.Id == nextRollRev.Id {
			foundNext = true
		}
	}
	if foundLast {
		return skerr.Fmt("Last roll rev %s found in not-rolled revs!", lastRollRev.Id)
	}
	if len(notRolledRevs) > 0 {
		if !foundTip {
			return skerr.Fmt("Tip rev %s not found in not-rolled revs!", tipRev.Id)
		}
		if !foundNext {
			return skerr.Fmt("Next roll rev %s not found in not-rolled revs!", nextRollRev.Id)
		}
	} else {
		if tipRev.Id != lastRollRev.Id {
			return skerr.Fmt("No revisions to roll, but tip rev %s does not equal last-rolled rev %s", tipRev.Id, lastRollRev.Id)
		}
		if nextRollRev.Id != lastRollRev.Id {
			return skerr.Fmt("No revisions to roll, but next roll rev is: %s", nextRollRev.Id)
		}
	}

	// Store the revs.
	r.statusMtx.Lock()
	defer r.statusMtx.Unlock()
	r.lastRollRev = lastRollRev
	r.nextRollRev = nextRollRev
	r.notRolledRevs = notRolledRevs
	r.tipRev = tipRev
	return nil
}

// Update the status information of the roller.
func (r *AutoRoller) updateStatus(ctx context.Context, replaceLastError bool, lastError string) error {
	r.statusMtx.Lock()
	defer r.statusMtx.Unlock()

	recent := r.recent.GetRecentRolls()
	numFailures := 0
	for _, roll := range recent {
		if roll.Failed() {
			numFailures++
		} else if roll.Succeeded() {
			break
		}
	}
	if !replaceLastError {
		lastError = r.status.Get().Error
	}

	failureThrottledUntil := r.failureThrottle.ThrottledUntil().Unix()
	safetyThrottledUntil := r.safetyThrottle.ThrottledUntil().Unix()
	successThrottledUntil := r.successThrottle.ThrottledUntil().Unix()
	throttledUntil := failureThrottledUntil
	if safetyThrottledUntil > throttledUntil {
		throttledUntil = safetyThrottledUntil
	}
	if successThrottledUntil > throttledUntil {
		throttledUntil = successThrottledUntil
	}

	notRolledRevs := r.notRolledRevs
	numNotRolled := len(notRolledRevs)
	sklog.Infof("Updating status (%d revisions behind)", numNotRolled)
	if numNotRolled > MAX_NOT_ROLLED_REVS {
		notRolledRevs = notRolledRevs[:1]
		sklog.Warningf("Truncating NotRolledRevisions; %d is more than the maximum of %d", numNotRolled, MAX_NOT_ROLLED_REVS)
	}
	currentRollRev := ""
	currentRoll := r.recent.CurrentRoll()
	if currentRoll != nil {
		currentRollRev = currentRoll.RollingTo
	}
	if err := status.Set(ctx, r.roller, &status.AutoRollStatus{
		AutoRollMiniStatus: status.AutoRollMiniStatus{
			CurrentRollRev:      currentRollRev,
			LastRollRev:         r.lastRollRev.Id,
			Mode:                r.GetMode(),
			NumFailedRolls:      numFailures,
			NumNotRolledCommits: numNotRolled,
		},
		ChildName:          r.childName,
		CurrentRoll:        r.recent.CurrentRoll(),
		Error:              lastError,
		FullHistoryUrl:     r.codereview.GetFullHistoryUrl(),
		IssueUrlBase:       r.codereview.GetIssueUrlBase(),
		LastRoll:           r.recent.LastRoll(),
		NotRolledRevisions: notRolledRevs,
		Recent:             recent,
		Status:             string(r.sm.Current()),
		ThrottledUntil:     throttledUntil,
		ValidModes:         modes.VALID_MODES,
		ValidStrategies:    r.cfg.ValidStrategies(),
	}); err != nil {
		return err
	}
	return r.status.Update(ctx)
}

// Run one iteration of the roller.
func (r *AutoRoller) Tick(ctx context.Context) error {
	r.runningMtx.Lock()
	defer r.runningMtx.Unlock()

	sklog.Infof("Running autoroller.")

	// Determine if we should unthrottle.
	shouldUnthrottle, err := unthrottle.Get(ctx, r.roller)
	if err != nil {
		return skerr.Wrapf(err, "Failed to determine whether we should unthrottle")
	}
	if shouldUnthrottle {
		if err := r.unthrottle(ctx); err != nil {
			return skerr.Wrapf(err, "Failed to unthrottle")
		}
		if err := unthrottle.Reset(ctx, r.roller); err != nil {
			return skerr.Wrapf(err, "Failed to reset unthrottle counter")
		}
	}

	// Update modes and strategies.
	if err := r.modeHistory.Update(ctx); err != nil {
		return skerr.Wrapf(err, "Failed to update mode history")
	}
	oldStrategy := r.strategyHistory.CurrentStrategy().Strategy
	if err := r.strategyHistory.Update(ctx); err != nil {
		return skerr.Wrapf(err, "Failed to update strategy history")
	}
	newStrategy := r.strategyHistory.CurrentStrategy().Strategy
	if oldStrategy != newStrategy {
		strat, err := strategy.GetNextRollStrategy(newStrategy)
		if err != nil {
			return skerr.Wrapf(err, "Failed to get next roll strategy")
		}
		r.strategyMtx.Lock()
		r.strategy = strat
		r.strategyMtx.Unlock()
	}

	// Run the state machine.
	lastErr := r.sm.NextTransitionSequence(ctx)
	lastErrStr := ""
	if lastErr != nil {
		lastErrStr = lastErr.Error()
	}

	// Update the status information.
	if err := r.updateStatus(ctx, true, lastErrStr); err != nil {
		return skerr.Wrapf(err, "Failed to update status")
	}
	sklog.Infof("Autoroller state %s", r.sm.Current())
	if lastRoll := r.recent.LastRoll(); lastRoll != nil && util.In(lastRoll.Result, []string{autoroll.ROLL_RESULT_DRY_RUN_SUCCESS, autoroll.ROLL_RESULT_SUCCESS}) {
		r.liveness.ManualReset(lastRoll.Modified)
	}
	return skerr.Wrapf(lastErr, "Failed state transition sequence")
}

// Add a comment to the given roll CL.
func (r *AutoRoller) AddComment(ctx context.Context, issueNum int64, message, user string, timestamp time.Time) error {
	roll, err := r.recent.Get(ctx, issueNum)
	if err != nil {
		return skerr.Fmt("No such issue %d", issueNum)
	}
	id := fmt.Sprintf("%d_%d", issueNum, len(roll.Comments))
	roll.Comments = append(roll.Comments, comment.New(id, message, user))
	return r.recent.Update(ctx, roll)
}

// Required for main.AutoRollerI. No specific HTTP handlers.
func (r *AutoRoller) AddHandlers(*mux.Router) {}

// Callback function which runs when roll CLs are closed.
func (r *AutoRoller) rollFinished(ctx context.Context, justFinished codereview.RollImpl) error {
	recent := r.recent.GetRecentRolls()
	// Sanity check: pop any rolls which occurred after the one which just
	// finished.
	idx := -1
	var currentRoll *autoroll.AutoRollIssue
	for i, roll := range recent {
		issue := fmt.Sprintf("%d", roll.Issue)
		if issue == justFinished.IssueID() {
			idx = i
			currentRoll = roll
			break
		}
	}
	if currentRoll == nil {
		return skerr.Fmt("Unable to find just-finished roll %q in recent list!", justFinished.IssueID())
	}

	// Feed AutoRoll stats into metrics.
	v := int64(0)
	if currentRoll.Closed && currentRoll.Committed {
		v = int64(1)
	}
	metrics2.GetInt64Metric("autoroll_last_roll_result", map[string]string{"roller": r.cfg.RollerName}).Update(v)

	recent = recent[idx:]
	var lastRoll *autoroll.AutoRollIssue
	if len(recent) > 1 {
		lastRoll = recent[1]
	} else {
		// If there are no other rolls, then the below alerts do not apply.
		return nil
	}

	issueURL := fmt.Sprintf("%s%d", r.codereview.GetIssueUrlBase(), currentRoll.Issue)

	// Send notifications if this roll had a different result from the last
	// roll, ie. success -> failure or failure -> success.
	currentSuccess := util.In(currentRoll.Result, autoroll.SUCCESS_RESULTS)
	lastSuccess := util.In(lastRoll.Result, autoroll.SUCCESS_RESULTS)
	if lastRoll != nil {
		if currentSuccess && !lastSuccess {
			r.notifier.SendNewSuccess(ctx, fmt.Sprintf("%d", currentRoll.Issue), issueURL)
		} else if !currentSuccess && lastSuccess {
			r.notifier.SendNewFailure(ctx, fmt.Sprintf("%d", currentRoll.Issue), issueURL)
		}
	}

	// Send a notification if the last N rolls failed in a row.
	nFailed := 0
	// recent is in reverse chronological order.
	for _, roll := range recent {
		if util.In(roll.Result, autoroll.SUCCESS_RESULTS) {
			break
		} else {
			nFailed++
		}
	}
	if nFailed == NOTIFY_IF_LAST_N_FAILED {
		r.notifier.SendLastNFailed(ctx, NOTIFY_IF_LAST_N_FAILED, issueURL)
	}

	return nil
}

// Handle manual roll requests.
func (r *AutoRoller) handleManualRolls(ctx context.Context) error {
	sklog.Infof("Searching manual roll requests for %s", r.cfg.RollerName)
	reqs, err := r.manualRollDB.GetIncomplete(r.cfg.RollerName)
	if err != nil {
		return skerr.Wrapf(err, "Failed to get incomplete rolls")
	}
	sklog.Infof("Found %d requests.", len(reqs))
	for _, req := range reqs {
		var issue *autoroll.AutoRollIssue
		to, err := r.getRevision(ctx, req.Revision)
		if err != nil {
			sklog.Errorf("Manual roll failed to obtain revision %q; marking failed: %s", req.Revision, err)
			req.Status = manual.STATUS_COMPLETE
			req.Result = manual.RESULT_FAILURE
			if err := r.manualRollDB.Put(req); err != nil {
				return skerr.Wrapf(err, "Failed to update manual roll request")
			}
			continue
		}
		if req.Status == manual.STATUS_PENDING {
			emails := r.GetEmails()
			if !util.In(req.Requester, emails) {
				emails = append(emails, req.Requester)
			}
			var err error
			sklog.Infof("Creating manual roll to %s as requested by %s...", req.Revision, req.Requester)
			from := r.GetCurrentRev()
			issue, err = r.createNewRoll(ctx, from, to, emails, false)
			if err != nil {
				return skerr.Wrapf(err, "Failed to create manual roll for %s: %s", req.Id, err)
			}
		} else if req.Status == manual.STATUS_STARTED {
			split := strings.Split(req.Url, "/")
			i, err := strconv.Atoi(split[len(split)-1])
			if err != nil {
				return skerr.Wrapf(err, "Failed to parse issue number from %s for %s: %s", req.Url, req.Id, err)
			}
			issue = &autoroll.AutoRollIssue{
				RollingTo: req.Revision,
				Issue:     int64(i),
			}
		} else {
			sklog.Errorf("Found manual roll request %s in unknown status %q", req.Id, req.Status)
			continue
		}
		sklog.Infof("Getting status for manual roll # %d", issue.Issue)
		roll, err := r.retrieveRoll(ctx, issue, to)
		if err != nil {
			return skerr.Wrapf(err, "Failed to retrieve manual roll %s: %s", req.Id, err)
		}
		req.Status = manual.STATUS_STARTED
		req.Url = roll.IssueURL()
		if roll.IsFinished() {
			req.Status = manual.STATUS_COMPLETE
			if roll.IsSuccess() {
				req.Result = manual.RESULT_SUCCESS
			} else {
				req.Result = manual.RESULT_FAILURE
			}
		}
		if err := r.manualRollDB.Put(req); err != nil {
			return skerr.Wrapf(err, "Failed to update manual roll request")
		}
	}
	return nil
}

// getRevision retrieves the Revision with the given ID, attempting to avoid
// network and/or subprocesses.
func (r *AutoRoller) getRevision(ctx context.Context, id string) (*revision.Revision, error) {
	if id == r.lastRollRev.Id {
		return r.lastRollRev, nil
	}
	if id == r.nextRollRev.Id {
		return r.nextRollRev, nil
	}
	if id == r.tipRev.Id {
		return r.tipRev, nil
	}
	for _, rev := range r.notRolledRevs {
		if id == rev.Id {
			return rev, nil
		}
	}
	return r.rm.GetRevision(ctx, id)
}
