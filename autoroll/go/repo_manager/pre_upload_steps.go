package repo_manager

/*
   This file contains canned pre-upload steps for RepoManagers to use.
*/

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.skia.org/infra/go/cipd"
	"go.skia.org/infra/go/exec"
	"go.skia.org/infra/go/git"
	"go.skia.org/infra/go/go_install"
	"go.skia.org/infra/go/skerr"
	"go.skia.org/infra/go/sklog"

	"go.skia.org/infra/go/metrics2"
)

var cipdRoot = path.Join(os.TempDir(), "cipd")

// PreUploadStep is a function to be run after the roll is performed but before
// a CL is uploaded. The string slice parameter is the set of environment
// variables which should be used by the pre-upload step. The http.Client should
// be authenticated. The string parameter is the absolute path to the directory
// of the parent repo.
type PreUploadStep func(context.Context, []string, *http.Client, string) error

// Return the PreUploadStep with the given name.
func GetPreUploadStep(s string) (PreUploadStep, error) {
	rv, ok := map[string]PreUploadStep{
		"ANGLECodeGeneration":             ANGLECodeGeneration,
		"GoGenerateCipd":                  GoGenerateCipd,
		"TrainInfra":                      TrainInfra,
		"FlutterLicenseScripts":           FlutterLicenseScripts,
		"FlutterLicenseScriptsForFuchsia": FlutterLicenseScriptsForFuchsia,
	}[s]
	if !ok {
		return nil, fmt.Errorf("No such pre-upload step: %s", s)
	}
	return rv, nil
}

// Return the PreUploadSteps with the given names.
func GetPreUploadSteps(steps []string) ([]PreUploadStep, error) {
	rv := make([]PreUploadStep, 0, len(steps))
	for _, s := range steps {
		step, err := GetPreUploadStep(s)
		if err != nil {
			return nil, err
		}
		rv = append(rv, step)
	}
	return rv, nil
}

// Train the infra expectations.
func TrainInfra(ctx context.Context, env []string, client *http.Client, parentRepoDir string) error {
	// TODO(borenet): Should we plumb through --local and --workdir?
	sklog.Info("Installing Go...")
	_, goEnv, err := go_install.EnsureGo(ctx, client, cipdRoot)
	if err != nil {
		return err
	}
	envMap := make(map[string]string, len(goEnv)+len(env))
	for _, v := range env {
		split := strings.SplitN(v, "=", 2)
		if len(split) != 2 {
			return fmt.Errorf("Invalid environment variable: %q", v)
		}
		envMap[split[0]] = split[1]
	}
	for k, v := range goEnv {
		if k == "PATH" {
			oldPath := envMap["PATH"]
			if oldPath != "" {
				v += ":" + oldPath
			}
		}
		envMap[k] = v
	}
	envSlice := make([]string, 0, len(envMap))
	for k, v := range envMap {
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
	}
	sklog.Info("Training infra expectations...")
	if _, err := exec.RunCommand(ctx, &exec.Command{
		Name: "make",
		Args: []string{"train"},
		Dir:  path.Join(parentRepoDir, "infra", "bots"),
		Env:  envSlice,
	}); err != nil {
		return err
	}
	return nil
}

// Run the flutter license scripts as described in
// https://bugs.chromium.org/p/skia/issues/detail?id=7730#c6 and in
// https://github.com/flutter/engine/blob/master/tools/licenses/README.md
func FlutterLicenseScripts(ctx context.Context, _ []string, _ *http.Client, parentRepoDir string) error {
	licenseScriptFailure := int64(1)
	defer func() {
		metrics2.GetInt64Metric("flutter_license_script_failure", nil).Update(licenseScriptFailure)
	}()
	if err := flutterLicenseScripts(ctx, parentRepoDir, "licenses_skia"); err != nil {
		return err
	}
	licenseScriptFailure = 0
	return nil
}

// Run the flutter license scripts for fuchsia.
func FlutterLicenseScriptsForFuchsia(ctx context.Context, _ []string, _ *http.Client, parentRepoDir string) error {
	licenseScriptFailure := int64(1)
	defer func() {
		metrics2.GetInt64Metric("flutter_license_script_failure", nil).Update(licenseScriptFailure)
	}()
	if err := flutterLicenseScripts(ctx, parentRepoDir, "licenses_fuchsia"); err != nil {
		return err
	}
	licenseScriptFailure = 0
	return nil
}

func flutterLicenseScripts(ctx context.Context, parentRepoDir, licenseFileName string) error {
	sklog.Info("Running flutter license scripts.")
	binariesPath := filepath.Join(parentRepoDir, "..", "third_party", "dart", "tools", "sdks", "dart-sdk", "bin")

	// Step1: Run pub get.
	licenseToolsDir := filepath.Join(parentRepoDir, "tools", "licenses")
	pubBinary := filepath.Join(binariesPath, "pub")
	sklog.Info("Running put get.")
	if _, err := exec.RunCwd(ctx, licenseToolsDir, pubBinary, "get"); err != nil {
		return fmt.Errorf("Error when running pub get: %s", err)
	}

	// Step2: Clean out out/licenses. Things fail without this step.
	licensesOutDir := filepath.Join(parentRepoDir, "..", "out", "licenses")
	if err := os.RemoveAll(licensesOutDir); err != nil {
		return fmt.Errorf("Error when cleaning out/licenses: %s", err)
	}

	// Step3: Run dart license script to create new licenses.
	dartBinary := filepath.Join(binariesPath, "dart")
	licensesGoldenDir := filepath.Join(parentRepoDir, "ci", "licenses_golden")
	licenseCmd := []string{dartBinary, "lib/main.dart", "--src", "../../..", "--out", licensesOutDir, "--golden", licensesGoldenDir}
	sklog.Infof("Running %s", licenseCmd)
	if err := exec.Run(ctx, &exec.Command{
		Dir:            licenseToolsDir,
		Name:           licenseCmd[0],
		Args:           licenseCmd[1:],
		CombinedOutput: os.Stdout,
	}); err != nil {
		return fmt.Errorf("Error when running dart license script: %s", err)
	}

	// Step4: Check to see if the target license file was created in the out dir.
	//        It will be created if the hash changes.
	if _, err := os.Stat(filepath.Join(licensesOutDir, licenseFileName)); err == nil {
		sklog.Infof("Found %s", licenseFileName)

		// Step5: Copy from out dir to goldens dir. This is required for updating
		//        the release file in sky_engine/LICENSE.
		if _, err := exec.RunCwd(ctx, licenseToolsDir, "cp", filepath.Join(licensesOutDir, licenseFileName), filepath.Join(licensesGoldenDir, licenseFileName)); err != nil {
			return fmt.Errorf("Error when copying %s from out to golden dir: %s", licenseFileName, err)
		}
		// Step6: Capture diff of licenses_golden/${licenseFileName}.
		licensesDiffOutput, err := git.GitDir(licenseToolsDir).Git(ctx, "diff", "--no-ext-diff", filepath.Join(licensesGoldenDir, licenseFileName))
		if err != nil {
			return fmt.Errorf("Error when seeing diff of golden %s: %s", licenseFileName, err)
		}
		sklog.Infof("The licenses diff output is:\n%s", licensesDiffOutput)

		// Step7: Update sky_engine/LICENSE. It should always be run
		// according to https://github.com/flutter/engine/pull/4959#issuecomment-380222322
		updateLicenseCmd := []string{dartBinary, "lib/main.dart", "--release", "--src", "../../..", "--out", licensesOutDir}
		sklog.Infof("Running %s", updateLicenseCmd)
		releasesLicensePath := filepath.Join(parentRepoDir, "sky", "packages", "sky_engine", "LICENSE")
		outFile, err := os.Create(releasesLicensePath)
		if err != nil {
			return fmt.Errorf("Could not open %s: %s", releasesLicensePath, err)
		}
		if err := exec.Run(ctx, &exec.Command{
			Dir:    licenseToolsDir,
			Name:   updateLicenseCmd[0],
			Args:   updateLicenseCmd[1:],
			Stdout: outFile,
			Stderr: os.Stderr,
		}); err != nil {
			return fmt.Errorf("Error when running dart license script: %s", err)
		}
	}

	sklog.Info("Done running flutter license scripts.")
	return nil
}

// Run "go generate" in go/cipd.
func GoGenerateCipd(ctx context.Context, _ []string, client *http.Client, parentRepoDir string) error {
	// TODO(borenet): Should we plumb through --local and --workdir?
	sklog.Info("Installing Go...")
	goExc, goEnv, err := go_install.EnsureGo(ctx, client, cipdRoot)
	if err != nil {
		return err
	}

	// Also install the protoc asset. Use a different CIPD root dir to
	// prevent conflicts with the Go packages.
	protocRoot := path.Join(os.TempDir(), "cipd_protoc")
	if err := cipd.Ensure(ctx, client, protocRoot, cipd.PkgProtoc); err != nil {
		return err
	}

	envSlice := make([]string, 0, len(goEnv))
	for k, v := range goEnv {
		if k == "PATH" {
			// Construct PATH by adding protoc and the required PATH
			// entries for Go on to the existing PATH.
			v = path.Join(protocRoot, cipd.PkgProtoc.Path, "bin") + ":" + v + ":" + os.Getenv("PATH")
		}
		envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, v))
	}

	// Run go generate.
	generateDir := filepath.Join(parentRepoDir, "go", "cipd")
	sklog.Infof("Running 'go generate' in %s", generateDir)
	if _, err := exec.RunCommand(ctx, &exec.Command{
		Name: goExc,
		Args: []string{"generate"},
		Dir:  generateDir,
		Env:  envSlice,
	}); err != nil {
		return err
	}
	return nil
}

// Run the ANGLE code generation script.
func ANGLECodeGeneration(ctx context.Context, _ []string, client *http.Client, parentRepoDir string) error {
	sklog.Info("Running code generation script...")
	_, err := exec.RunCwd(ctx, parentRepoDir, "python", filepath.Join("scripts", "run_code_generation.py"))
	return skerr.Wrap(err)
}
