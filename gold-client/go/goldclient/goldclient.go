package goldclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/ioutil"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"go.skia.org/infra/go/fileutil"
	"go.skia.org/infra/go/skerr"
	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/go/util"
	"go.skia.org/infra/golden/go/baseline"
	"go.skia.org/infra/golden/go/diff"
	"go.skia.org/infra/golden/go/jsonio"
	"go.skia.org/infra/golden/go/shared"
	"go.skia.org/infra/golden/go/types"
	"go.skia.org/infra/golden/go/types/expectations"
	"go.skia.org/infra/golden/go/web/frontend"
)

const (
	// jsonPrefix is the path prefix in the GCS bucket that holds JSON result files
	jsonPrefix = "dm-json-v1"

	// imagePrefix is the path prefix in the GCS bucket that holds images.
	imagePrefix = "dm-images-v1"

	// knownHashesPath is path on the Gold instance to retrieve the known image hashes that do
	// not need to be uploaded anymore.
	knownHashesPath = "json/hashes"

	// stateFile is the name of the file that holds the state in the work directory
	// between calls
	stateFile = "result-state.json"

	// jsonTempFile is the temporary file that is created to upload results via gsutil.
	jsonTempFile = "dm.json"

	// goldHostTemplate constructs the URL of the Gold instance from the instance id
	goldHostTemplate = "https://%s-gold.skia.org"

	// bucketTemplate constructs the name of the ingestion bucket from the instance id
	bucketTemplate = "skia-gold-%s"
)

// md5Regexp is used to check whether strings are MD5 hashes.
var md5Regexp = regexp.MustCompile(`^[a-f0-9]{32}$`)

// GoldClient is the uniform interface to communicate with the Gold service.
type GoldClient interface {
	// SetSharedConfig populates the config with details that will be shared
	// with all tests. This is safe to be called more than once, although
	// new settings will overwrite the old ones. This will cause the
	// baseline and known hashes to be (re-)downloaded from Gold.
	SetSharedConfig(sharedConfig jsonio.GoldResults, skipValidation bool) error

	// Test adds a test result to the current test run. If the GoldClient is configured to
	// return PASS/FAIL for each test, the returned boolean indicates whether the test passed
	// comparison with the expectations (this involves uploading JSON to the server).
	// This will upload the image if the hash of the pixels has not been seen before -
	// using auth.SetDryRun(true) can prevent that.
	// additionalKeys is an optional set of key:value pairs that apply to only this test.
	// This is typically a small amount of data (and can be nil). If there are many keys,
	// they are likely shared between tests and should be added in SetSharedConfig.
	//
	// An error is only returned if there was a technical problem in processing the test.
	Test(name types.TestName, imgFileName string, additionalKeys map[string]string) (bool, error)

	// Check operates similarly to Test, except it does not persist anything about the call.
	// That is, the image will not be uploaded to Gold, only compared against the baseline.
	// Check returns true/false if the image is on the baseline or not.
	// An error is only returned if there was a technical problem in processing the test.
	Check(name types.TestName, imgFileName string) (bool, error)

	// Diff computes a diff of the closest image to the given image file and puts it into outDir,
	// along with the closest image file itself.
	Diff(ctx context.Context, name types.TestName, corpus, imgFileName, outDir string) error

	// Finalize uploads the JSON file for all Test() calls previously seen.
	// A no-op if configured for PASS/FAIL mode, since the JSON would have been uploaded
	// on the calls to Test().
	Finalize() error
}

// GoldClientDebug contains some "optional" methods that can assist
// in debugging.
type GoldClientDebug interface {
	// DumpBaseline returns a human-readable representation of the baseline as a string.
	// This is a set of test names that each have a set of image
	// digests that each have exactly one types.Label.
	DumpBaseline() (string, error)
	// DumpKnownHashes returns a human-readable representation of the known image digests
	// which is a list of hashes.
	DumpKnownHashes() (string, error)
}

// HTTPClient makes it easier to mock out goldclient's dependencies on
// http.Client by representing a smaller interface.
type HTTPClient interface {
	Get(url string) (resp *http.Response, err error)
}

// CloudClient implements the GoldClient interface for the remote Gold service.
type CloudClient struct {
	// workDir is a temporary directory that has to exist between related calls
	workDir string

	// resultState keeps track of the all the information to generate and upload a valid result.
	resultState *resultState

	// ready caches the result of the isReady call so we avoid duplicate work.
	ready bool

	// these functions are overwritable by tests
	loadAndHashImage func(path string) ([]byte, types.Digest, error)
	now              func() time.Time

	// auth stores the authentication method to use.
	auth       AuthOpt
	httpClient HTTPClient
}

// GoldClientConfig is a config structure to configure GoldClient instances
type GoldClientConfig struct {
	// WorkDir is a temporary directory that caches data for one run with multiple calls to GoldClient
	WorkDir string

	// InstanceID is the id of the backend Gold instance
	InstanceID string

	// PassFailStep indicates whether each call to Test(...) should return a pass/fail value.
	PassFailStep bool

	// FailureFile is a file on disk that will contain newline-seperated links to triage
	// any failures. Only written to if PassFailStep is true
	FailureFile string

	// OverrideGoldURL is optional and allows to override the GoldURL for testing.
	OverrideGoldURL string

	// UploadOnly is a mode where we don't check expectations against the server - i.e.
	// we just operate in upload mode.
	UploadOnly bool
}

// resultState is an internal container for all information to upload results
// to Gold, including the jsonio.GoldResult structure itself.
type resultState struct {
	// SharedConfig is all the data that is common test to test, for example, the
	// keys about this machine (e.g. GPU, OS).
	SharedConfig    *jsonio.GoldResults
	PerTestPassFail bool
	FailureFile     string
	UploadOnly      bool
	InstanceID      string
	GoldURL         string
	Bucket          string
	KnownHashes     types.DigestSet
	Expectations    expectations.Baseline
}

// NewCloudClient returns an implementation of the GoldClient that relies on the Gold service.
// If a new instance is created for each call to Test, the arguments of the first call are
// preserved. They are cached in a JSON file in the work directory.
func NewCloudClient(authOpt AuthOpt, config GoldClientConfig) (*CloudClient, error) {
	// Make sure the workdir was given and exists.
	if config.WorkDir == "" {
		return nil, skerr.Fmt("no 'workDir' provided to NewCloudClient")
	}

	workDir, err := fileutil.EnsureDirExists(config.WorkDir)
	if err != nil {
		return nil, skerr.Wrapf(err, "setting up workdir %q", config.WorkDir)
	}

	if config.InstanceID == "" {
		return nil, skerr.Fmt("empty config passed into NewCloudClient")
	}

	ret := CloudClient{
		workDir:          workDir,
		auth:             authOpt,
		loadAndHashImage: loadAndHashImage,
		now:              defaultNow,

		resultState: newResultState(nil, &config),
	}
	if err := ret.setHttpClient(); err != nil {
		return nil, skerr.Wrapf(err, "setting http client")
	}

	if config.FailureFile != "" {
		if f, err := os.Create(config.FailureFile); err != nil {
			return nil, skerr.Wrapf(err, "making failure file %s", config.FailureFile)
		} else {
			if err := f.Close(); err != nil {
				return nil, skerr.Wrapf(err, "closing failure file %s", config.FailureFile)
			}
		}
	}

	// write it to disk
	if err := saveJSONFile(ret.getResultStatePath(), ret.resultState); err != nil {
		return nil, skerr.Wrapf(err, "writing the state to disk")
	}

	return &ret, nil
}

// LoadCloudClient returns a GoldClient that has previously been stored to disk
// in the path given by workDir.
func LoadCloudClient(authOpt AuthOpt, workDir string) (*CloudClient, error) {
	// Make sure the workdir was given and exists.
	if workDir == "" {
		return nil, skerr.Fmt("No 'workDir' provided to LoadCloudClient")
	}
	ret := CloudClient{
		workDir:          workDir,
		auth:             authOpt,
		loadAndHashImage: loadAndHashImage,
		now:              defaultNow,
	}
	var err error
	ret.resultState, err = loadStateFromJSON(ret.getResultStatePath())
	if err != nil {
		return nil, skerr.Wrapf(err, "loading state from disk")
	}
	if err = ret.setHttpClient(); err != nil {
		return nil, skerr.Wrapf(err, "setting http client")
	}

	return &ret, nil
}

// SetSharedConfig implements the GoldClient interface.
func (c *CloudClient) SetSharedConfig(sharedConfig jsonio.GoldResults, skipValidation bool) error {
	if !skipValidation {
		if err := sharedConfig.Validate(true); err != nil {
			return skerr.Wrapf(err, "invalid configuration")
		}
	}

	existingConfig := GoldClientConfig{
		WorkDir: c.workDir,
	}
	if c.resultState != nil {
		existingConfig.InstanceID = c.resultState.InstanceID
		existingConfig.PassFailStep = c.resultState.PerTestPassFail
		existingConfig.FailureFile = c.resultState.FailureFile
		existingConfig.OverrideGoldURL = c.resultState.GoldURL
		existingConfig.UploadOnly = c.resultState.UploadOnly
	}
	c.resultState = newResultState(&sharedConfig, &existingConfig)

	if !c.resultState.UploadOnly {
		// The GitHash may have changed (or been set for the first time),
		// So we can now load the baseline. We can also download the hashes
		// at this time, although we could have done it at any time before since
		// that does not depend on the GitHash we have.
		if err := c.downloadHashesAndBaselineFromGold(); err != nil {
			return skerr.Wrapf(err, "downloading from Gold")
		}
	}

	return saveJSONFile(c.getResultStatePath(), c.resultState)
}

// Test implements the GoldClient interface.
func (c *CloudClient) Test(name types.TestName, imgFileName string, additionalKeys map[string]string) (bool, error) {
	if res, err := c.addTest(name, imgFileName, additionalKeys); err != nil {
		return false, err
	} else {
		return res, saveJSONFile(c.getResultStatePath(), c.resultState)
	}
}

// addTest adds a test to results. If perTestPassFail is true it will also upload the result.
// Returns true if the test was added (and maybe uploaded) successfully.
func (c *CloudClient) addTest(name types.TestName, imgFileName string, additionalKeys map[string]string) (bool, error) {
	if err := c.isReady(); err != nil {
		return false, skerr.Wrapf(err, "gold client not ready")
	}

	// Get an uploader. This is either based on an authenticated client or on gsutils.
	uploader, err := c.auth.GetGCSUploader()
	if err != nil {
		return false, skerr.Wrapf(err, "retrieving uploader")
	}

	// Load the PNG from disk and hash it.
	imgBytes, imgHash, err := c.loadAndHashImage(imgFileName)
	if err != nil {
		return false, skerr.Wrap(err)
	}

	// Add the result of this test.
	c.addResult(name, imgHash, additionalKeys)

	// At this point the result should be correct for uploading.
	if err := c.resultState.SharedConfig.Validate(false); err != nil {
		return false, skerr.Wrapf(err, "invalid test config")
	}

	fmt.Printf("Given image with hash %s for test %s\n", imgHash, name)
	for expectHash, expectLabel := range c.resultState.Expectations[name] {
		fmt.Printf("Expectation for test: %s (%s)\n", expectHash, expectLabel.String())
	}

	var egroup errgroup.Group
	// Check against known hashes and upload if needed.
	if !c.resultState.KnownHashes[imgHash] {
		egroup.Go(func() error {
			gcsImagePath := c.resultState.getGCSImagePath(imgHash)
			if err := uploader.UploadBytes(context.TODO(), imgBytes, imgFileName, gcsImagePath); err != nil {
				return skerr.Fmt("Error uploading image %s to %s. Got: %s", imgFileName, gcsImagePath, err)
			}
			return nil
		})
	}

	// If we do per test pass/fail then upload the result and compare it to the baseline.
	ret := true
	if c.resultState.PerTestPassFail {
		egroup.Go(func() error {
			return c.uploadResultJSON(uploader)
		})

		ret = c.resultState.Expectations[name][imgHash] == expectations.Positive
		if !ret {
			link := fmt.Sprintf("%s/detail?test=%s&digest=%s\n", c.resultState.GoldURL, name, imgHash)
			fmt.Printf("Untriaged or negative image: %s", link)
			ff := c.resultState.FailureFile
			if ff != "" {
				egroup.Go(func() error {
					f, err := os.OpenFile(ff, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
					if err != nil {
						return skerr.Fmt("could not open failure file %s: %s", ff, err)
					}
					if _, err := f.WriteString(link); err != nil {
						return skerr.Fmt("could not write to failure file %s: %s", ff, err)
					}
					if err := f.Close(); err != nil {
						return skerr.Fmt("could not close failure file %s: %s", ff, err)
					}
					return nil
				})
			}
		}
	}

	if err := egroup.Wait(); err != nil {
		return false, err
	}
	return ret, nil
}

// Check implements the GoldClient interface.
func (c *CloudClient) Check(name types.TestName, imgFileName string) (bool, error) {
	if len(c.resultState.Expectations) == 0 {
		if err := c.downloadHashesAndBaselineFromGold(); err != nil {
			return false, skerr.Wrapf(err, "fetching baseline")
		}
		if err := saveJSONFile(c.getResultStatePath(), c.resultState); err != nil {
			return false, skerr.Wrapf(err, "writing the expectations to disk")
		}
	}
	// Load the PNG from disk and hash it.
	_, imgHash, err := c.loadAndHashImage(imgFileName)
	if err != nil {
		return false, skerr.Wrap(err)
	}
	fmt.Printf("Given image with hash %s for test %s\n", imgHash, name)
	for expectHash, expectLabel := range c.resultState.Expectations[name] {
		fmt.Printf("Expectation for test: %s (%s)\n", expectHash, expectLabel.String())
	}
	ret := c.resultState.Expectations[name][imgHash] == expectations.Positive
	return ret, nil
}

// Finalize implements the GoldClient interface.
func (c *CloudClient) Finalize() error {
	if err := c.isReady(); err != nil {
		return skerr.Fmt("Cannot finalize - client not ready: %s", err)
	}
	uploader, err := c.auth.GetGCSUploader()
	if err != nil {
		return skerr.Fmt("Error retrieving uploader: %s", err)
	}
	return c.uploadResultJSON(uploader)
}

// uploadResultJSON uploads the results (which live in SharedConfig, specifically
// SharedConfig.Results), to GCS.
func (c *CloudClient) uploadResultJSON(uploader GCSUploader) error {
	localFileName := filepath.Join(c.workDir, jsonTempFile)
	resultFilePath := c.resultState.getResultFilePath(c.now())
	if err := uploader.UploadJSON(context.TODO(), c.resultState.SharedConfig, localFileName, resultFilePath); err != nil {
		return skerr.Fmt("Error uploading JSON file to GCS path %s: %s", resultFilePath, err)
	}
	return nil
}

// setHttpClient sets authenticated httpClient, if authentication was configured via SetAuthConfig.
// It also retrieves a token of the configured source to make sure it works.
func (c *CloudClient) setHttpClient() error {
	// If no auth option was set, we return an unauthenticated client.
	client, err := c.auth.GetHTTPClient()
	if err != nil {
		return err
	}
	c.httpClient = client
	return nil
}

// saveAuthOpt assumes that auth has been set. It saves it to the work directory for retrieval
// during later calls.
func (c *CloudClient) saveAuthOpt() error {
	outFile := filepath.Join(c.workDir, authFile)
	return saveJSONFile(outFile, c.auth)
}

// isReady returns true if the instance is ready to accept test results (all necessary info has been
// configured)
func (c *CloudClient) isReady() error {
	if c.ready {
		return nil
	}

	// if resultState hasn't been set yet, then we are simply not ready.
	if c.resultState == nil {
		return skerr.Fmt("No result state object available")
	}

	// Check whether we have some means of uploading results
	if c.auth == nil {
		return skerr.Fmt("No authentication information provided.")
	}
	if err := c.auth.Validate(); err != nil {
		return skerr.Fmt("Invalid auth: %s", err)
	}

	// Check if the GoldResults instance is complete once results are added.
	if err := c.resultState.SharedConfig.Validate(true); err != nil {
		return skerr.Wrapf(err, "Gold results fields invalid")
	}

	c.ready = true
	return nil
}

// getResultStatePath returns the path of the temporary file where the state is cached as JSON
func (c *CloudClient) getResultStatePath() string {
	return filepath.Join(c.workDir, stateFile)
}

// addResult adds the given test to the overall results.
func (c *CloudClient) addResult(name types.TestName, imgHash types.Digest, additionalKeys map[string]string) {
	newResult := &jsonio.Result{
		Digest: imgHash,
		Key:    map[string]string{types.PRIMARY_KEY_FIELD: string(name)},

		// We need to specify this is a png, otherwise the backend will refuse
		// to ingest it.
		Options: map[string]string{"ext": "png"},
	}
	for k, v := range additionalKeys {
		newResult.Key[k] = v
	}

	// Set the CORPUS_FIELD (e.g. source_type) to the default value of the instanceID
	// if it is not set either on Key (via init) or additionalKeys (via add)
	if c.resultState.SharedConfig.Key[types.CORPUS_FIELD] == "" && newResult.Key[types.CORPUS_FIELD] == "" {
		newResult.Key[types.CORPUS_FIELD] = c.resultState.InstanceID
	}
	c.resultState.SharedConfig.Results = append(c.resultState.SharedConfig.Results, newResult)
}

// downloadHashesAndBaselineFromGold downloads the hashes and baselines
// and stores them to resultState.
func (c *CloudClient) downloadHashesAndBaselineFromGold() error {
	// What hashes have we seen already (to avoid uploading them again).
	if err := c.resultState.loadKnownHashes(c.httpClient); err != nil {
		return err
	}

	fmt.Printf("Loaded %d known hashes\n", len(c.resultState.KnownHashes))

	// Fetch the baseline (may be empty but should not fail).
	if err := c.resultState.loadExpectations(c.httpClient); err != nil {
		return err
	}
	fmt.Printf("Loaded %d tests from the baseline\n", len(c.resultState.Expectations))

	return nil
}

// loadAndHashImage loads an image from disk and hashes the internal Pixel buffer. It returns
// the bytes of the encoded image and the MD5 hash of the pixels as hex encoded string.
func loadAndHashImage(fileName string) ([]byte, types.Digest, error) {
	// Load the image and save the bytes because we need to return them.
	imgBytes, err := ioutil.ReadFile(fileName)
	if err != nil {
		return nil, "", skerr.Wrapf(err, "loading file %s", fileName)
	}
	img, err := png.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		return nil, "", skerr.Wrapf(err, "decoding PNG in file %s", fileName)
	}
	nrgbaImg := diff.GetNRGBA(img)
	// hash it
	s := md5.Sum(nrgbaImg.Pix)
	md5Hash := hex.EncodeToString(s[:])
	return imgBytes, types.Digest(md5Hash), nil
}

// defaultNow returns what time it is now in UTC
func defaultNow() time.Time {
	return time.Now().UTC()
}

// newResultState creates a new instance of resultState
func newResultState(sharedConfig *jsonio.GoldResults, config *GoldClientConfig) *resultState {
	goldURL := config.OverrideGoldURL
	if goldURL == "" {
		goldURL = getHostURL(config.InstanceID)
	}

	ret := &resultState{
		SharedConfig:    sharedConfig,
		PerTestPassFail: config.PassFailStep,
		FailureFile:     config.FailureFile,
		InstanceID:      config.InstanceID,
		UploadOnly:      config.UploadOnly,
		GoldURL:         goldURL,
		Bucket:          getBucket(config.InstanceID),
	}

	return ret
}

// loadStateFromJSON loads a serialization of a resultState instance that was previously written
// via the save method.
func loadStateFromJSON(fileName string) (*resultState, error) {
	ret := &resultState{}
	exists, err := loadJSONFile(fileName, ret)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, skerr.Fmt("The state file %q doesn't exist.", fileName)
	}
	return ret, nil
}

const maxAttempts = 5

// getWithRetries makes a get request with retries to work around the rare
// unexpected EOF error. See https://crbug.com/skia/9108
// httpClient should do retries with an exponential backoff
// for transient failures - this covers other failures.
// TODO(kjlubick) add context.Context
func getWithRetries(httpClient HTTPClient, url string) ([]byte, error) {
	var lastErr error

	for attempts := 0; attempts < maxAttempts; attempts++ {
		if lastErr != nil {
			fmt.Printf("Retry attempt #%d after error: %s\n", attempts, lastErr)
			// reset the error
			lastErr = nil

			// Sleep to give the server time to recover, if needed.
			time.Sleep(time.Duration(500+rand.Int31n(1000)) * time.Millisecond)
		}

		// wrap in a function to make sure the defer resp.Body.Close() can
		// happen before we try again.
		b, err := func() ([]byte, error) {
			resp, err := httpClient.Get(url)
			if err != nil {
				return nil, skerr.Fmt("error on get %s: %s", url, err)
			}

			if resp.StatusCode >= http.StatusBadRequest {
				return nil, skerr.Fmt("GET %s resulted in a %d: %s", url, resp.StatusCode, resp.Status)
			}
			defer func() {
				if err := resp.Body.Close(); err != nil {
					fmt.Printf("Warning while closing HTTP response for %s: %s", url, err)
				}
			}()
			b, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				return nil, skerr.Fmt("error reading body %s: %s", url, err)
			}
			return b, nil
		}()
		if err != nil {
			lastErr = err
			continue
		}
		return b, nil
	}
	return nil, lastErr
}

// loadKnownHashes loads the list of known hashes from the Gold instance.
func (r *resultState) loadKnownHashes(httpClient HTTPClient) error {
	r.KnownHashes = types.DigestSet{}

	// Fetch the known hashes via http
	hashesURL := fmt.Sprintf("%s/%s", r.GoldURL, knownHashesPath)
	body, err := getWithRetries(httpClient, hashesURL)
	if err != nil {
		return skerr.Wrapf(err, "getting known hashes from %s (with retries)", hashesURL)
	}

	scanner := bufio.NewScanner(bytes.NewBuffer(body))
	for scanner.Scan() {
		// Ignore empty lines and lines that are not valid MD5 hashes
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) > 0 && md5Regexp.Match(line) {
			r.KnownHashes[types.Digest(line)] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return skerr.Wrapf(err, "scanning response of HTTP request")
	}
	return nil
}

// loadExpectations fetches the expectations from Gold to compare to tests.
func (r *resultState) loadExpectations(httpClient HTTPClient) error {
	urlPath := strings.Replace(shared.ExpectationsRoute, "{commit_hash}", "HEAD", 1)
	if r.SharedConfig != nil {
		urlPath = strings.Replace(shared.ExpectationsRoute, "{commit_hash}", r.SharedConfig.GitHash, 1)
		if r.SharedConfig.ChangeListID != "" {
			urlPath = fmt.Sprintf("%s?issue=%s", urlPath, r.SharedConfig.ChangeListID)
		}
	}
	url := fmt.Sprintf("%s/%s", r.GoldURL, strings.TrimLeft(urlPath, "/"))

	jsonBytes, err := getWithRetries(httpClient, url)
	if err != nil {
		return skerr.Wrapf(err, "getting expectations from %s (with retries)", url)
	}

	exp := &baseline.Baseline{}

	if err := json.Unmarshal(jsonBytes, exp); err != nil {
		fmt.Printf("Fetched from %s\n", url)
		if len(jsonBytes) > 200 {
			fmt.Printf(`Invalid JSON: "%s..."`, string(jsonBytes[0:200]))
		} else {
			fmt.Printf(`Invalid JSON: "%s"`, string(jsonBytes))
		}
		return skerr.Wrapf(err, "parsing JSON; this sometimes means auth issues")
	}

	r.Expectations = exp.Expectations
	return nil
}

// getResultFilePath returns that path in GCS where the result file should be stored.
//
// The path follows the path described here:
//    https://github.com/google/skia-buildbot/blob/master/golden/docs/INGESTION.md
// The file name of the path also contains a timestamp to make it unique since all
// calls within the same test run are written to the same output path.
func (r *resultState) getResultFilePath(now time.Time) string {
	year, month, day := now.Date()
	hour := now.Hour()

	// Assemble a path that looks like this:
	// <path_prefix>/YYYY/MM/DD/HH/<git_hash>/<build_id>/<time_stamp>/<per_run_file_name>.json
	// The first segments up to 'HH' are required so the Gold ingester can scan these prefixes for
	// new files. The later segments are necessary to make the path unique within the runs of one
	// hour and increase readability of the paths for troubleshooting.
	// It is vital that the times segments of the path are based on UTC location.
	fileName := fmt.Sprintf("dm-%d.json", now.UnixNano())
	builder := r.SharedConfig.TryJobID
	if builder == "" {
		builder = "waterfall"
	}
	segments := []interface{}{
		jsonPrefix,
		year,
		month,
		day,
		hour,
		r.SharedConfig.GitHash,
		builder,
		now.Unix(),
		fileName}
	path := fmt.Sprintf("%s/%04d/%02d/%02d/%02d/%s/%s/%d/%s", segments...)

	if r.SharedConfig.ChangeListID != "" {
		path = "trybot/" + path
	}
	return fmt.Sprintf("%s/%s", r.Bucket, path)
}

// getGCSImagePath returns the path in GCS where the image with the given hash should be stored.
func (r *resultState) getGCSImagePath(imgHash types.Digest) string {
	return fmt.Sprintf("%s%s/%s/%s.png", gcsPrefix, r.Bucket, imagePrefix, imgHash)
}

// loadJSONFile loads and parses the JSON in 'fileName'. If the file doesn't exist it returns
// (false, nil). If the first return value is true, 'data' contains the parse JSON data.
func loadJSONFile(fileName string, data interface{}) (bool, error) {
	if !fileutil.FileExists(fileName) {
		return false, nil
	}

	err := util.WithReadFile(fileName, func(r io.Reader) error {
		return json.NewDecoder(r).Decode(data)
	})
	if err != nil {
		return false, skerr.Wrapf(err, "reading/parsing JSON file: %s", fileName)
	}

	return true, nil
}

// saveJSONFile stores the given 'data' in a file with the given name
func saveJSONFile(fileName string, data interface{}) error {
	err := util.WithWriteFile(fileName, func(w io.Writer) error {
		return json.NewEncoder(w).Encode(data)
	})
	if err != nil {
		return skerr.Wrapf(err, "writing/serializing to JSON file %s", fileName)
	}
	return nil
}

const (
	// Skia's naming conventions are old and don't follow the patterns that
	// newer clients do. One day, it might be nice to align the skia names
	// to match the rest.
	bucketSkiaLegacy     = "skia-infra-gm"
	hostSkiaLegacy       = "https://gold.skia.org"
	instanceIDSkiaLegacy = "skia-legacy"

	hostFuchsiaCorp   = "https://fuchsia-gold.corp.goog"
	instanceIDFuchsia = "fuchsia"
)

// getBucket returns the bucket name for a given instance id.
// This is usually a formulaic transform, but there are some special cases.
func getBucket(instanceID string) string {
	if instanceID == instanceIDSkiaLegacy {
		return bucketSkiaLegacy
	}
	return fmt.Sprintf(bucketTemplate, instanceID)
}

// getHostURL returns the hostname for a given instance id.
// This is usually a formulaic transform, but there are some special cases.
func getHostURL(instanceID string) string {
	if instanceID == instanceIDSkiaLegacy {
		return hostSkiaLegacy
	}
	if instanceID == instanceIDFuchsia {
		return hostFuchsiaCorp
	}
	return fmt.Sprintf(goldHostTemplate, instanceID)
}

// DumpBaseline fulfills the GoldClientDebug interface
func (c *CloudClient) DumpBaseline() (string, error) {
	if c.resultState == nil || c.resultState.Expectations == nil {
		return "", errors.New("Not instantiated - call init?")
	}
	return stringifyBaseline(c.resultState.Expectations), nil
}

func stringifyBaseline(b map[types.TestName]map[types.Digest]expectations.Label) string {
	names := make([]string, 0, len(b))
	for testName := range b {
		names = append(names, string(testName))
	}
	sort.Strings(names)
	s := strings.Builder{}
	for _, testName := range names {
		digestMap := b[types.TestName(testName)]
		digests := make([]string, 0, len(digestMap))
		for d := range digestMap {
			digests = append(digests, string(d))
		}
		sort.Strings(digests)
		_, _ = fmt.Fprintf(&s, "%s:\n", testName)
		for _, d := range digests {
			_, _ = fmt.Fprintf(&s, "\t%s : %s\n", d, digestMap[types.Digest(d)].String())
		}
	}
	return s.String()
}

// DumpKnownHashes fulfills the GoldClientDebug interface
func (c *CloudClient) DumpKnownHashes() (string, error) {
	if c.resultState == nil || c.resultState.KnownHashes == nil {
		return "", errors.New("Not instantiated - call init?")
	}
	var hashes []string
	for h := range c.resultState.KnownHashes {
		hashes = append(hashes, string(h))
	}
	sort.Strings(hashes)
	s := strings.Builder{}
	_, _ = s.WriteString("Hashes:\n\t")
	_, _ = s.WriteString(strings.Join(hashes, "\n\t"))
	_, _ = s.WriteString("\n")
	return s.String(), nil
}

// Diff fulfills the GoldClient interface.
func (c *CloudClient) Diff(ctx context.Context, name types.TestName, corpus, imgFileName, outDir string) error {
	if err := os.MkdirAll(outDir, os.ModePerm); err != nil {
		return skerr.Wrapf(err, "creating outdir %s", outDir)
	}

	// 1) Read in file, write it to outdir/ using correct hash.
	b, inputDigest, err := c.loadAndHashImage(imgFileName)
	if err != nil {
		return skerr.Wrapf(err, "reading input %s", imgFileName)
	}

	origFilePath := filepath.Join(outDir, fmt.Sprintf("input-%s.png", inputDigest))
	if err := ioutil.WriteFile(origFilePath, b, 0644); err != nil {
		return skerr.Wrapf(err, "writing to %s", origFilePath)
	}

	leftImg, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return skerr.Wrapf(err, "reading %s as png", origFilePath)
	}

	// 2) Check JSON endpoint digests to download
	u := fmt.Sprintf("%s/json/digests?test=%s&corpus=%s", c.resultState.GoldURL, name, corpus)
	jb, err := getWithRetries(c.httpClient, u)
	if err != nil {
		return skerr.Wrapf(err, "reading images for test %s, corpus %s from gold (url: %s)", name, corpus, u)
	}

	var dlr frontend.DigestListResponse
	if err := json.Unmarshal(jb, &dlr); err != nil {
		return skerr.Wrapf(err, "invalid JSON from digests served from %s: %s", u, string(jb))
	}
	if len(dlr.Digests) == 0 {
		sklog.Warningf("Gold doesn't know of any digests that match %s and corpus %s", name, corpus)
		return nil
	}

	sklog.Infof("Going to compare %s.png against %d other images", inputDigest, len(dlr.Digests))

	// 3a) Download those from bucket (or use from working directory cache). We download them with
	//    the same credentials that let us upload them.
	digestsPath := filepath.Join(c.workDir, "digests")
	if err := os.MkdirAll(digestsPath, os.ModePerm); err != nil {
		return skerr.Wrapf(err, "creating digests directory %s", digestsPath)
	}

	smallestCombined := float32(math.MaxFloat32)
	var closestRightDigest types.Digest
	// we have to keep the raw bytes (i.e. we cannot simply re-encoded closestDiffImg to disk)
	// because golang does not support fancy image things like color spaces, so re-encoding it
	// would lose that data.
	var closestRightImg []byte
	var closestDiffImg image.Image
	for _, d := range dlr.Digests {
		db, err := c.getEncodedDigestFromCacheOrGCS(ctx, d, digestsPath)
		if err != nil {
			return skerr.Wrap(err)
		}
		rightImg, err := png.Decode(bytes.NewReader(db))
		if err != nil {
			return skerr.Wrapf(err, "Invalid PNG stored in digest %s (cached at %s)", d, digestsPath)
		}

		// 3b) Compare each of the images to the given image, looking for the smallest combined
		//     diff metric.
		dm, diffImg := diff.PixelDiff(leftImg, rightImg)
		cdm := diff.CombinedDiffMetric(dm, nil, nil)
		if cdm < smallestCombined {
			smallestCombined = cdm
			closestDiffImg = diffImg
			closestRightImg = db
			closestRightDigest = d
		}
	}
	sklog.Infof("Digest %s was closest (combined metric of %f)", closestRightDigest, smallestCombined)

	// 4) Write closest image and the diff to that image to the output directory.
	o := filepath.Join(outDir, fmt.Sprintf("closest-%s.png", closestRightDigest))
	if err := ioutil.WriteFile(o, closestRightImg, 0644); err != nil {
		return skerr.Wrapf(err, "writing closest image to %s", o)
	}

	o = filepath.Join(outDir, "diff.png")
	diffFile, err := os.Create(o)
	if err != nil {
		return skerr.Wrapf(err, "opening %s for writing", o)
	}
	if err := png.Encode(diffFile, closestDiffImg); err != nil {
		return skerr.Wrapf(err, "encoding diff image")
	}

	return skerr.Wrap(diffFile.Close())
}

// getEncodedDigestFromCacheOrGCS returns the encoded PNG bytes for a digest from GCS or
// from the local cache (e.g. cachePath).
func (c *CloudClient) getEncodedDigestFromCacheOrGCS(ctx context.Context, d types.Digest, cachePath string) ([]byte, error) {
	p := filepath.Join(cachePath, string(d)+".png")
	if b, err := ioutil.ReadFile(p); err == nil {
		return b, nil
	} else if !os.IsNotExist(err) {
		return nil, skerr.Wrapf(err, "unexpected error accessing %s", p)
	}
	// File wasn't on disk, so we need to download it, write it there and return it.
	dl, err := c.auth.GetGCSDownloader()
	if err != nil {
		return nil, skerr.Wrap(err)
	}
	img := c.resultState.getGCSImagePath(d)
	b, err := dl.Download(ctx, img, cachePath)
	if err != nil {
		return nil, skerr.Wrapf(err, "downloading %s", img)
	}
	return b, skerr.Wrapf(ioutil.WriteFile(p, b, 0644), "caching to %s", p)
}

// Make sure CloudClient fulfills the GoldClient interface
var _ GoldClient = (*CloudClient)(nil)

// Make sure CloudClient fulfills the GoldClientDebug interface
var _ GoldClientDebug = (*CloudClient)(nil)
