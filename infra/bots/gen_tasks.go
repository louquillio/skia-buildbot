// Copyright 2016 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

/*
	Generate the tasks.json file.
*/

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"go.skia.org/infra/go/sklog"
	"go.skia.org/infra/task_scheduler/go/specs"
)

const (
	BUILD_TASK_DRIVERS_NAME = "Housekeeper-PerCommit-BuildTaskDrivers"
	BUNDLE_RECIPES_NAME     = "Housekeeper-PerCommit-BundleRecipes"

	DEFAULT_OS       = DEFAULT_OS_LINUX
	DEFAULT_OS_LINUX = "Debian-9.8"
	DEFAULT_OS_WIN   = "Windows-2016Server-14393"

	LOGDOG_ANNOTATION_URL = "logdog://logs.chromium.org/skia/${SWARMING_TASK_ID}/+/annotations"

	// Small is a 2-core machine.
	MACHINE_TYPE_SMALL = "n1-highmem-2"
	// Medium is a 16-core machine
	MACHINE_TYPE_MEDIUM = "n1-standard-16"
	// Large is a 64-core machine.
	MACHINE_TYPE_LARGE = "n1-highcpu-64"

	// Swarming output dirs.
	OUTPUT_NONE = "output_ignored" // This will result in outputs not being isolated.

	// Pool for Skia bots.
	POOL_SKIA = "Skia"

	SERVICE_ACCOUNT_COMPILE       = "skia-external-compile-tasks@skia-swarming-bots.iam.gserviceaccount.com"
	SERVICE_ACCOUNT_RECREATE_SKPS = "skia-recreate-skps@skia-swarming-bots.iam.gserviceaccount.com"
)

var (
	// "Constants"

	// Top-level list of all Jobs to run at each commit.
	JOBS = []string{
		"Housekeeper-Nightly-UpdateGoDeps",
		"Housekeeper-Weekly-UpdateCIPDPackages",
		"Housekeeper-OnDemand-Presubmit",
		"Infra-PerCommit-Build",
		"Infra-PerCommit-Small",
		"Infra-PerCommit-Medium",
		"Infra-PerCommit-Large",
		"Infra-PerCommit-Race",
		"Infra-Experimental-Small-Linux",
		"Infra-Experimental-Small-Win",
	}

	CACHES_GO = []*specs.Cache{
		{
			Name: "go_cache",
			Path: "cache/go_cache",
		},
		{
			Name: "gopath",
			Path: "cache/gopath",
		},
	}

	// These properties are required by some tasks, eg. for running
	// bot_update, but they prevent de-duplication, so they should only be
	// used where necessary.
	EXTRA_PROPS = map[string]string{
		"buildbucket_build_id": specs.PLACEHOLDER_BUILDBUCKET_BUILD_ID,
		"patch_issue":          specs.PLACEHOLDER_ISSUE_INT,
		"patch_ref":            specs.PLACEHOLDER_PATCH_REF,
		"patch_repo":           specs.PLACEHOLDER_PATCH_REPO,
		"patch_set":            specs.PLACEHOLDER_PATCHSET_INT,
		"patch_storage":        specs.PLACEHOLDER_PATCH_STORAGE,
		"repository":           specs.PLACEHOLDER_REPO,
		"revision":             specs.PLACEHOLDER_REVISION,
		"task_id":              specs.PLACEHOLDER_TASK_ID,
	}
)

// relpath returns the relative path to the given file from the config file.
func relpath(f string) string {
	_, filename, _, _ := runtime.Caller(0)
	dir := path.Dir(filename)
	rv, err := filepath.Rel(dir, path.Join(dir, f))
	if err != nil {
		sklog.Fatal(err)
	}
	return rv
}

// Dimensions for Linux GCE instances.
func linuxGceDimensions(machineType string) []string {
	return []string{
		"pool:Skia",
		fmt.Sprintf("os:%s", DEFAULT_OS_LINUX),
		"gpu:none",
		"cpu:x86-64-Haswell_GCE",
		fmt.Sprintf("machine_type:%s", machineType),
	}
}

// Dimensions for Windows GCE instances.
func winGceDimensions(machineType string) []string {
	return []string{
		"pool:Skia",
		fmt.Sprintf("os:%s", DEFAULT_OS_WIN),
		"gpu:none",
		"cpu:x86-64-Haswell_GCE",
		fmt.Sprintf("machine_type:%s", machineType),
	}
}

// Apply the default CIPD packages.
func cipd(pkgs []*specs.CipdPackage) []*specs.CipdPackage {
	// We also need Git.
	rv := append(specs.CIPD_PKGS_KITCHEN, specs.CIPD_PKGS_GIT...)
	return append(rv, pkgs...)
}

// Create a properties JSON string.
func props(p map[string]string) string {
	d := make(map[string]interface{}, len(p)+1)
	for k, v := range p {
		d[k] = interface{}(v)
	}
	d["$kitchen"] = struct {
		DevShell bool `json:"devshell"`
		GitAuth  bool `json:"git_auth"`
	}{
		DevShell: true,
		GitAuth:  true,
	}

	j, err := json.Marshal(d)
	if err != nil {
		sklog.Fatal(err)
	}
	return strings.Replace(string(j), "\\u003c", "<", -1)
}

// bundleRecipes generates the task to bundle and isolate the recipes.
func bundleRecipes(b *specs.TasksCfgBuilder) string {
	b.MustAddTask(BUNDLE_RECIPES_NAME, &specs.TaskSpec{
		CipdPackages: append(specs.CIPD_PKGS_GIT, specs.CIPD_PKGS_PYTHON...),
		Command: []string{
			"/bin/bash", "buildbot/infra/bots/bundle_recipes.sh", specs.PLACEHOLDER_ISOLATED_OUTDIR,
		},
		Dimensions: linuxGceDimensions(MACHINE_TYPE_SMALL),
		EnvPrefixes: map[string][]string{
			"PATH": {"cipd_bin_packages", "cipd_bin_packages/bin"},
		},
		Idempotent: true,
		Isolate:    "recipes.isolate",
	})
	return BUNDLE_RECIPES_NAME
}

// buildTaskDrivers generates the task to compile the task driver code to run on
// a given platform.
func buildTaskDrivers(b *specs.TasksCfgBuilder, os, arch string) string {
	// TODO(borenet): Add support for RPI.
	goos := map[string]string{
		"Linux": "linux",
		"Mac":   "darwin",
		"Win":   "windows",
	}[os]
	goarch := map[string]string{
		"x86":    "386",
		"x86_64": "amd64",
	}[arch]
	name := fmt.Sprintf("%s-%s-%s", BUILD_TASK_DRIVERS_NAME, os, arch)
	b.MustAddTask(name, &specs.TaskSpec{
		Caches:       CACHES_GO,
		CipdPackages: append(specs.CIPD_PKGS_GIT, b.MustGetCipdPackageFromAsset("go")),
		Command: []string{
			"/bin/bash", "buildbot/infra/bots/build_task_drivers.sh", specs.PLACEHOLDER_ISOLATED_OUTDIR,
		},
		Dimensions: linuxGceDimensions(MACHINE_TYPE_SMALL),
		Environment: map[string]string{
			"GOOS":   goos,
			"GOARCH": goarch,
		},
		EnvPrefixes: map[string][]string{
			"PATH": {"cipd_bin_packages", "cipd_bin_packages/bin", "go/go/bin"},
		},
		// This task is idempotent but unlikely to ever be deduped
		// because it depends on the entire repo...
		Idempotent: true,
		Isolate:    "whole_repo.isolate",
	})
	return name

}

// kitchenTask returns a specs.TaskSpec instance which uses Kitchen to run a
// recipe.
func kitchenTask(name, recipe, isolate, serviceAccount string, dimensions []string, extraProps map[string]string, outputDir string) *specs.TaskSpec {
	cipd := append([]*specs.CipdPackage{}, specs.CIPD_PKGS_KITCHEN...)
	properties := map[string]string{
		"buildername":   name,
		"swarm_out_dir": specs.PLACEHOLDER_ISOLATED_OUTDIR,
	}
	for k, v := range extraProps {
		properties[k] = v
	}
	var outputs []string = nil
	if outputDir != OUTPUT_NONE {
		outputs = []string{outputDir}
	}
	python := "cipd_bin_packages/vpython${EXECUTABLE_SUFFIX}"
	return &specs.TaskSpec{
		Caches: []*specs.Cache{
			{
				Name: "vpython",
				Path: "cache/vpython",
			},
		},
		CipdPackages: cipd,
		Command:      []string{python, "-u", "buildbot/infra/bots/run_recipe.py", "${ISOLATED_OUTDIR}", recipe, props(properties), "skia"},
		Dependencies: []string{BUNDLE_RECIPES_NAME},
		Dimensions:   dimensions,
		EnvPrefixes: map[string][]string{
			"PATH":                    {"cipd_bin_packages", "cipd_bin_packages/bin"},
			"VPYTHON_VIRTUALENV_ROOT": {"${cache_dir}/vpython"},
		},
		ExtraTags: map[string]string{
			"log_location": LOGDOG_ANNOTATION_URL,
		},
		Isolate:        isolate,
		Outputs:        outputs,
		ServiceAccount: serviceAccount,
	}
}

// infra generates an infra test Task. Returns the name of the last Task in the
// generated chain of Tasks, which the Job should add as a dependency.
func infra(b *specs.TasksCfgBuilder, name string) string {
	machineType := MACHINE_TYPE_MEDIUM
	if strings.Contains(name, "Large") {
		// Using MACHINE_TYPE_LARGE for Large tests saves ~2 minutes.
		machineType = MACHINE_TYPE_LARGE
	}
	task := kitchenTask(name, "swarm_infra", "whole_repo.isolate", SERVICE_ACCOUNT_COMPILE, linuxGceDimensions(machineType), nil, OUTPUT_NONE)
	task.CipdPackages = append(task.CipdPackages, specs.CIPD_PKGS_GIT...)
	task.CipdPackages = append(task.CipdPackages, b.MustGetCipdPackageFromAsset("go"))
	task.Caches = append(task.Caches, CACHES_GO...)
	task.CipdPackages = append(task.CipdPackages, b.MustGetCipdPackageFromAsset("node"))
	task.CipdPackages = append(task.CipdPackages, specs.CIPD_PKGS_GSUTIL...)
	if strings.Contains(name, "Large") || strings.Contains(name, "Build") {
		task.CipdPackages = append(task.CipdPackages, b.MustGetCipdPackageFromAsset("protoc"))
	}

	// Cloud datastore tests are assumed to be marked as 'Large'
	if strings.Contains(name, "Large") || strings.Contains(name, "Race") {
		task.CipdPackages = append(task.CipdPackages, specs.CIPD_PKGS_ISOLATE...)
		task.CipdPackages = append(task.CipdPackages, b.MustGetCipdPackageFromAsset("gcloud_linux"))
	}

	// Re-run failing bots but not when testing for race conditions.
	task.MaxAttempts = 2
	if strings.Contains(name, "Race") {
		task.MaxAttempts = 1
		task.IoTimeout = 1 * time.Hour
	}
	b.MustAddTask(name, task)
	return name
}

// Run the presubmit.
func presubmit(b *specs.TasksCfgBuilder, name string) string {
	extraProps := map[string]string{
		"category":         "cq",
		"patch_gerrit_url": "https://skia-review.googlesource.com",
		"patch_project":    "buildbot",
		"patch_ref":        fmt.Sprintf("refs/changes/%s/%s/%s", specs.PLACEHOLDER_ISSUE_SHORT, specs.PLACEHOLDER_ISSUE, specs.PLACEHOLDER_PATCHSET),
		"reason":           "CQ",
		"repo_name":        "skia_buildbot",
	}
	for k, v := range EXTRA_PROPS {
		extraProps[k] = v
	}
	task := kitchenTask(name, "run_presubmit", "run_recipe.isolate", SERVICE_ACCOUNT_COMPILE, linuxGceDimensions(MACHINE_TYPE_MEDIUM), extraProps, OUTPUT_NONE)
	task.Caches = append(task.Caches, []*specs.Cache{
		{
			Name: "git",
			Path: "cache/git",
		},
		{
			Name: "git_cache",
			Path: "cache/git_cache",
		},
	}...)
	task.CipdPackages = append(task.CipdPackages, specs.CIPD_PKGS_GIT...)
	task.CipdPackages = append(task.CipdPackages, &specs.CipdPackage{
		Name:    "infra/recipe_bundles/chromium.googlesource.com/chromium/tools/build",
		Path:    "recipe_bundle",
		Version: "git_revision:617e0fd3186eaae8bcb7521def0d6d3b4a5bcaf1",
	})
	task.Dependencies = []string{} // No bundled recipes for this one.
	b.MustAddTask(name, task)
	return name
}

func experimental(b *specs.TasksCfgBuilder, name string) string {
	cipd := append([]*specs.CipdPackage{}, specs.CIPD_PKGS_GIT...)
	cipd = append(cipd, specs.CIPD_PKGS_GSUTIL...)
	cipd = append(cipd, specs.CIPD_PKGS_PYTHON...)
	cipd = append(cipd, b.MustGetCipdPackageFromAsset("node"))

	machineType := MACHINE_TYPE_MEDIUM
	if strings.Contains(name, "Large") {
		// Using MACHINE_TYPE_LARGE for Large tests saves ~2 minutes.
		machineType = MACHINE_TYPE_LARGE
		cipd = append(cipd, b.MustGetCipdPackageFromAsset("protoc"))
	}

	var deps []string
	var dims []string
	if strings.Contains(name, "Win") {
		goPkg := b.MustGetCipdPackageFromAsset("go_win")
		goPkg.Path = "go"
		cipd = append(cipd, goPkg)
		deps = append(deps, buildTaskDrivers(b, "Win", "x86_64"))
		dims = winGceDimensions(machineType)
	} else if strings.Contains(name, "Linux") {
		cipd = append(cipd, b.MustGetCipdPackageFromAsset("go"))
		deps = append(deps, buildTaskDrivers(b, "Linux", "x86_64"))
		dims = linuxGceDimensions(machineType)
	}
	t := &specs.TaskSpec{
		Caches:       CACHES_GO,
		CipdPackages: cipd,
		Command: []string{
			"./infra_tests",
			"--project_id", "skia-swarming-bots",
			"--task_id", specs.PLACEHOLDER_TASK_ID,
			"--task_name", name,
			"--workdir", ".",
			"--alsologtostderr",
		},
		Dependencies: deps,
		Dimensions:   dims,
		EnvPrefixes: map[string][]string{
			"PATH": {"cipd_bin_packages", "cipd_bin_packages/bin", "go/go/bin"},
		},
		Isolate:        "whole_repo.isolate",
		ServiceAccount: SERVICE_ACCOUNT_COMPILE,
	}
	b.MustAddTask(name, t)
	return name
}

func updateGoDeps(b *specs.TasksCfgBuilder, name string) string {
	cipd := append([]*specs.CipdPackage{}, specs.CIPD_PKGS_GIT...)
	cipd = append(cipd, b.MustGetCipdPackageFromAsset("go"))
	cipd = append(cipd, b.MustGetCipdPackageFromAsset("protoc"))

	machineType := MACHINE_TYPE_MEDIUM
	t := &specs.TaskSpec{
		Caches:       CACHES_GO,
		CipdPackages: cipd,
		Command: []string{
			"./update_go_deps",
			"--project_id", "skia-swarming-bots",
			"--task_id", specs.PLACEHOLDER_TASK_ID,
			"--task_name", name,
			"--workdir", ".",
			"--gerrit_project", "buildbot",
			"--gerrit_url", "https://skia-review.googlesource.com",
			"--repo", specs.PLACEHOLDER_REPO,
			"--reviewers", "borenet@google.com",
			"--revision", specs.PLACEHOLDER_REVISION,
			"--patch_issue", specs.PLACEHOLDER_ISSUE,
			"--patch_set", specs.PLACEHOLDER_PATCHSET,
			"--patch_server", specs.PLACEHOLDER_CODEREVIEW_SERVER,
			"--alsologtostderr",
		},
		Dependencies: []string{buildTaskDrivers(b, "Linux", "x86_64")},
		Dimensions:   linuxGceDimensions(machineType),
		EnvPrefixes: map[string][]string{
			"PATH": {"cipd_bin_packages", "cipd_bin_packages/bin", "go/go/bin"},
		},
		Isolate:        "empty.isolate",
		ServiceAccount: SERVICE_ACCOUNT_RECREATE_SKPS,
	}
	b.MustAddTask(name, t)
	return name
}

func updateCIPDPackages(b *specs.TasksCfgBuilder, name string) string {
	cipd := append([]*specs.CipdPackage{}, specs.CIPD_PKGS_GIT...)
	cipd = append(cipd, b.MustGetCipdPackageFromAsset("go"))
	cipd = append(cipd, b.MustGetCipdPackageFromAsset("protoc"))

	machineType := MACHINE_TYPE_MEDIUM
	t := &specs.TaskSpec{
		Caches:       CACHES_GO,
		CipdPackages: cipd,
		Command: []string{
			"./roll_cipd_packages",
			"--project_id", "skia-swarming-bots",
			"--task_id", specs.PLACEHOLDER_TASK_ID,
			"--task_name", name,
			"--workdir", ".",
			"--gerrit_project", "buildbot",
			"--gerrit_url", "https://skia-review.googlesource.com",
			"--repo", specs.PLACEHOLDER_REPO,
			"--reviewers", "borenet@google.com",
			"--revision", specs.PLACEHOLDER_REVISION,
			"--patch_issue", specs.PLACEHOLDER_ISSUE,
			"--patch_set", specs.PLACEHOLDER_PATCHSET,
			"--patch_server", specs.PLACEHOLDER_CODEREVIEW_SERVER,
			"--alsologtostderr",
		},
		Dependencies: []string{buildTaskDrivers(b, "Linux", "x86_64")},
		Dimensions:   linuxGceDimensions(machineType),
		EnvPrefixes: map[string][]string{
			"PATH": {"cipd_bin_packages", "cipd_bin_packages/bin", "go/go/bin"},
		},
		Isolate:        "empty.isolate",
		ServiceAccount: SERVICE_ACCOUNT_RECREATE_SKPS,
	}
	b.MustAddTask(name, t)
	return name
}

// process generates Tasks and Jobs for the given Job name.
func process(b *specs.TasksCfgBuilder, name string) {
	var priority float64 // Leave as default for most jobs.
	deps := []string{}

	if strings.Contains(name, "Experimental") {
		// Experimental recipe-less tasks.
		deps = append(deps, experimental(b, name))
	} else if strings.Contains(name, "UpdateGoDeps") {
		// Update Go deps bot.
		deps = append(deps, updateGoDeps(b, name))
	} else if strings.Contains(name, "UpdateCIPDPackages") {
		// Update CIPD packages bot.
		deps = append(deps, updateCIPDPackages(b, name))
	} else {
		// Infra tests.
		if strings.Contains(name, "Infra-PerCommit") {
			deps = append(deps, infra(b, name))
		}
		// Presubmit.
		if strings.Contains(name, "Presubmit") {
			priority = 1
			deps = append(deps, presubmit(b, name))
		}
	}

	// Add the Job spec.
	trigger := specs.TRIGGER_ANY_BRANCH
	if strings.Contains(name, "OnDemand") {
		trigger = specs.TRIGGER_ON_DEMAND
	} else if strings.Contains(name, "Nightly") {
		trigger = specs.TRIGGER_NIGHTLY
	} else if strings.Contains(name, "Weekly") {
		trigger = specs.TRIGGER_WEEKLY
	}
	b.MustAddJob(name, &specs.JobSpec{
		Priority:  priority,
		TaskSpecs: deps,
		Trigger:   trigger,
	})
}

// Regenerate the tasks.json file.
func main() {
	b := specs.MustNewTasksCfgBuilder()

	// Create Tasks and Jobs.
	bundleRecipes(b)
	for _, name := range JOBS {
		process(b, name)
	}

	b.MustFinish()
}
